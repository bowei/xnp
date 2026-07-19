package eqc

type Labeled interface {
	ID() uint64
	Namespace() NsID
	Labels() LabelSet
}

type EqC struct {
	relevant    []KeyID
	relevantSet map[KeyID]bool

	partitions map[NsID]Partitions

	// nextLabelSetID is the source of new LabelSetIDs. The content of
	// each interned LabelSet lives only in labelSetByKey's string keys
	// (and, for groups currently non-empty, in their Partition.labels) —
	// there is no separate id->LabelSet map, since nothing needs reverse
	// lookup by id.
	nextLabelSetID LabelSetID
	labelSetByKey  map[string]LabelSetID

	// locations tracks where each (namespace, object ID) currently lives
	// (its partition and index within that partition's slice) so
	// Remove/Update can find and evict a single object in O(1) instead
	// of scanning every partition/object. Keyed by the (ns, id) pair
	// rather than nested per-namespace maps — one flat map has less
	// overhead than many small ones when there are many namespaces, and
	// keying on the pair (not just id) preserves the property that the
	// same object ID left behind in two namespaces (the documented
	// caller-error case in Update) is tracked independently in each,
	// rather than one clobbering the other's location and orphaning it.
	locations map[locKey]objLoc
}

type locKey struct {
	ns NsID
	id uint64
}

// objLoc is where a single object lives within its namespace's
// Partitions: which LabelSetID-keyed partition, and its index in that
// partition's objects slice.
type objLoc struct {
	labelSetID LabelSetID
	idx        int
}

type Partitions map[LabelSetID]Partition

type Partition struct {
	labels     LabelSet
	labelSetID LabelSetID
	objects    []Labeled
}

// Labels is the (relevant-projected) LabelSet shared by every object in
// this Partition.
func (p Partition) Labels() LabelSet { return p.labels }

// LabelSetID is the identity assigned to p.Labels().
func (p Partition) LabelSetID() LabelSetID { return p.labelSetID }

// Objects returns every object currently in this Partition.
func (p Partition) Objects() []Labeled { return p.objects }

// New returns a new EqC using the given relevant keys to determine the
// equivalence classes.
func New(relevant []KeyID) *EqC {
	return &EqC{
		relevant:      append([]KeyID(nil), relevant...),
		relevantSet:   newRelevantSet(relevant),
		partitions:    map[NsID]Partitions{},
		labelSetByKey: map[string]LabelSetID{},
		locations:     map[locKey]objLoc{},
	}
}

func newRelevantSet(relevant []KeyID) map[KeyID]bool {
	set := make(map[KeyID]bool, len(relevant))
	for _, k := range relevant {
		set[k] = true
	}
	return set
}

// project is the internal equivalent of LabelSet.Project(c.relevant),
// using the precomputed c.relevantSet instead of rebuilding a key set on
// every call — relevant only changes on UpdateRelevant, not per object.
func (c *EqC) project(s LabelSet) LabelSet {
	out := make(LabelSet, 0, len(s))
	for _, kv := range s {
		if c.relevantSet[kv.K()] {
			out = append(out, kv)
		}
	}
	return out
}

// Add objects to the EqC, splitting the objects based on their
// relevant Labels.
func (c *EqC) Add(objects []Labeled) {
	var keyBuf []byte
	for _, o := range objects {
		labels := o.Labels()
		keyBuf = labels.appendProjectKey(keyBuf[:0], c.relevantSet)
		id, isNew := c.internKey(keyBuf)

		ns := o.Namespace()
		nsPartitions, ok := c.partitions[ns]
		if !ok {
			nsPartitions = Partitions{}
			c.partitions[ns] = nsPartitions
		}

		p := nsPartitions[id]
		if isNew {
			// Only the first object seen for this group needs to pay
			// for materializing the projected LabelSet; every later
			// object in the same group just reuses p.labels.
			p.labels = c.project(labels)
			p.labelSetID = id
		}
		p.objects = append(p.objects, o)
		nsPartitions[id] = p
		c.locations[locKey{ns, o.ID()}] = objLoc{labelSetID: id, idx: len(p.objects) - 1}
	}
}

// Update objects if their labels have changed. Each o is looked up by ID
// within its current Namespace()'s existing Partitions — Partition.Labels()
// is the projection frozen at Add time, so comparing it against o's
// freshly computed projection detects drift without needing the object's
// prior label state. Objects not currently tracked under that namespace are
// just Added. Update does not detect a changed Namespace: a relabel that
// also moves an object to a different namespace still needs an explicit
// Remove of the old value followed by an Add of the new one.
func (c *EqC) Update(objects []Labeled) {
	for _, o := range objects {
		ns := o.Namespace()
		loc, ok := c.locations[locKey{ns, o.ID()}]
		if !ok {
			c.Add([]Labeled{o})
			continue
		}

		nsPartitions := c.partitions[ns]
		p := nsPartitions[loc.labelSetID]
		if o.Labels().projectEquals(c.relevantSet, p.labels) {
			p.objects[loc.idx] = o
			nsPartitions[loc.labelSetID] = p
			continue
		}

		c.evict(ns, o.ID(), loc)
		c.Add([]Labeled{o})
	}
}

// Remove objects from the EqC. Each object is located via the ID-keyed
// index recorded at Add time, so this does not depend on the labels
// passed on o still matching what was stored (unlike Update, a stale
// label snapshot on o does not cause a mislocated or skipped removal).
func (c *EqC) Remove(objects []Labeled) {
	for _, o := range objects {
		ns := o.Namespace()
		loc, ok := c.locations[locKey{ns, o.ID()}]
		if !ok {
			continue
		}
		c.evict(ns, o.ID(), loc)
	}
}

// evict removes the object with the given id at loc from its partition
// in namespace ns, in O(1): swap the last object into loc's slot (fixing
// up the moved object's tracked index) and truncate.
func (c *EqC) evict(ns NsID, id uint64, loc objLoc) {
	nsPartitions := c.partitions[ns]
	p := nsPartitions[loc.labelSetID]

	last := len(p.objects) - 1
	if loc.idx != last {
		p.objects[loc.idx] = p.objects[last]
		c.locations[locKey{ns, p.objects[loc.idx].ID()}] = objLoc{labelSetID: loc.labelSetID, idx: loc.idx}
	}
	p.objects = p.objects[:last]
	delete(c.locations, locKey{ns, id})

	if len(p.objects) == 0 {
		delete(nsPartitions, loc.labelSetID)
		if len(nsPartitions) == 0 {
			delete(c.partitions, ns)
		}
	} else {
		nsPartitions[loc.labelSetID] = p
	}
}

// UpdateRelevant labels.
//
// * Labels that have been removed can cause Partitions to be merged
// * Labels that have been added can cause Partitions to be split.
func (c *EqC) UpdateRelevant(labels []KeyID) {
	var all []Labeled
	for _, nsPartitions := range c.partitions {
		for _, p := range nsPartitions {
			all = append(all, p.objects...)
		}
	}

	c.relevant = append([]KeyID(nil), labels...)
	c.relevantSet = newRelevantSet(labels)
	c.partitions = map[NsID]Partitions{}
	c.nextLabelSetID = 0
	c.labelSetByKey = map[string]LabelSetID{}
	c.locations = map[locKey]objLoc{}
	c.Add(all)
}

// Range returns an iterator over the NsID and Partitions.
func (c *EqC) Range() func(yield func(NsID, Partitions) bool) {
	return func(yield func(NsID, Partitions) bool) {
		for ns, p := range c.partitions {
			if !yield(ns, p) {
				return
			}
		}
	}
}

// findLabelSetID returns the LabelSetID already assigned to ls's content,
// if any.
func (c *EqC) findLabelSetID(ls LabelSet) (LabelSetID, bool) {
	id, ok := c.labelSetByKey[ls.key()]
	return id, ok
}

// internKey returns the LabelSetID already assigned to the content
// encoded by key (see LabelSet.key / LabelSet.projectKey), allocating a
// new one the first time this exact (relevant-projected) label set is
// seen and reporting isNew so the caller knows whether it still needs to
// materialize/store the actual LabelSet. Identity is content-addressed,
// not namespace-scoped: two namespaces with the same projected labels
// share a LabelSetID.
//
// The map lookup on `string(key)` is the compiler's well-known
// zero-allocation idiom for []byte-keyed map reads (see
// https://github.com/golang/go/issues/3512): key itself is only copied
// into a real string on the (rare, one-per-distinct-group) insert path.
func (c *EqC) internKey(key []byte) (id LabelSetID, isNew bool) {
	if id, ok := c.labelSetByKey[string(key)]; ok {
		return id, false
	}
	id = c.nextLabelSetID
	c.nextLabelSetID++
	c.labelSetByKey[string(key)] = id
	return id, true
}
