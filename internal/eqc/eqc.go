package eqc

import "slices"

type Labeled interface {
	ID() uint64
	Namespace() NsID
	Labels() LabelSet
}

type EqC struct {
	relevant    []KeyID
	relevantSet map[KeyID]bool

	partitions map[NsID]Partitions

	labeSetIDs    map[LabelSetID]LabelSet
	labelSetByKey map[string]LabelSetID

	// locations tracks, per namespace, where each object currently
	// lives (its partition and index within that partition's slice) so
	// Remove/Update can find and evict a single object in O(1) instead
	// of scanning every partition/object.
	locations map[NsID]map[uint64]objLoc
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
		labeSetIDs:    map[LabelSetID]LabelSet{},
		labelSetByKey: map[string]LabelSetID{},
		locations:     map[NsID]map[uint64]objLoc{},
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
		if c.relevantSet[kv.K] {
			out = append(out, kv)
		}
	}
	return out
}

// Add objects to the EqC, splitting the objects based on their
// relevant Labels.
func (c *EqC) Add(objects []Labeled) {
	for _, o := range objects {
		proj := c.project(o.Labels())
		id := c.internLabelSet(proj)

		ns := o.Namespace()
		nsPartitions, ok := c.partitions[ns]
		if !ok {
			nsPartitions = Partitions{}
			c.partitions[ns] = nsPartitions
		}
		nsLocations, ok := c.locations[ns]
		if !ok {
			nsLocations = map[uint64]objLoc{}
			c.locations[ns] = nsLocations
		}

		p := nsPartitions[id]
		p.labels = proj
		p.labelSetID = id
		p.objects = append(p.objects, o)
		nsPartitions[id] = p
		nsLocations[o.ID()] = objLoc{labelSetID: id, idx: len(p.objects) - 1}
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
		nsLocations, ok := c.locations[ns]
		if !ok {
			c.Add([]Labeled{o})
			continue
		}
		loc, ok := nsLocations[o.ID()]
		if !ok {
			c.Add([]Labeled{o})
			continue
		}

		newProj := c.project(o.Labels())
		nsPartitions := c.partitions[ns]
		p := nsPartitions[loc.labelSetID]
		if slices.Equal(p.labels, newProj) {
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
		nsLocations, ok := c.locations[ns]
		if !ok {
			continue
		}
		loc, ok := nsLocations[o.ID()]
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
	nsLocations := c.locations[ns]

	last := len(p.objects) - 1
	if loc.idx != last {
		p.objects[loc.idx] = p.objects[last]
		nsLocations[p.objects[loc.idx].ID()] = objLoc{labelSetID: loc.labelSetID, idx: loc.idx}
	}
	p.objects = p.objects[:last]
	delete(nsLocations, id)

	if len(p.objects) == 0 {
		delete(nsPartitions, loc.labelSetID)
		if len(nsPartitions) == 0 {
			delete(c.partitions, ns)
			delete(c.locations, ns)
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
	c.labeSetIDs = map[LabelSetID]LabelSet{}
	c.labelSetByKey = map[string]LabelSetID{}
	c.locations = map[NsID]map[uint64]objLoc{}
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

// internLabelSet returns the LabelSetID for ls's content, allocating a new
// one the first time this exact (relevant-projected) label set is seen.
// Identity is content-addressed, not namespace-scoped: two namespaces with
// the same projected labels share a LabelSetID.
func (c *EqC) internLabelSet(ls LabelSet) LabelSetID {
	key := ls.key()
	if id, ok := c.labelSetByKey[key]; ok {
		return id
	}
	id := LabelSetID(len(c.labeSetIDs))
	c.labeSetIDs[id] = ls
	c.labelSetByKey[key] = id
	return id
}
