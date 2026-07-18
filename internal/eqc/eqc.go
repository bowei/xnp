package eqc

import "slices"

type Labeled interface {
	ID() uint64
	Namespace() NsID
	Labels() LabelSet
}

type EqC struct {
	relevant   []KeyID
	partitions map[NsID]Partitions
	labeSetIDs map[LabelSetID]LabelSet
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
		relevant:   append([]KeyID(nil), relevant...),
		partitions: map[NsID]Partitions{},
		labeSetIDs: map[LabelSetID]LabelSet{},
	}
}

// Add objects to the EqC, splitting the objects based on their
// relevant Labels.
func (c *EqC) Add(objects []Labeled) {
	for _, o := range objects {
		proj := o.Labels().Project(c.relevant)
		id := c.internLabelSet(proj)

		ns := o.Namespace()
		nsPartitions, ok := c.partitions[ns]
		if !ok {
			nsPartitions = Partitions{}
			c.partitions[ns] = nsPartitions
		}

		p := nsPartitions[id]
		p.labels = proj
		p.labelSetID = id
		p.objects = append(p.objects, o)
		nsPartitions[id] = p
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
		nsPartitions, ok := c.partitions[ns]
		if !ok {
			c.Add([]Labeled{o})
			continue
		}

		var oldID LabelSetID
		found := false
		for id, p := range nsPartitions {
			if slices.ContainsFunc(p.objects, func(other Labeled) bool { return other.ID() == o.ID() }) {
				oldID = id
				found = true
				break
			}
		}
		if !found {
			c.Add([]Labeled{o})
			continue
		}

		newProj := o.Labels().Project(c.relevant)
		p := nsPartitions[oldID]
		if slices.Equal(p.labels, newProj) {
			for j, existing := range p.objects {
				if existing.ID() == o.ID() {
					p.objects[j] = o
					break
				}
			}
			continue
		}

		p.objects = slices.DeleteFunc(p.objects, func(other Labeled) bool {
			return other.ID() == o.ID()
		})
		if len(p.objects) == 0 {
			delete(nsPartitions, oldID)
			if len(nsPartitions) == 0 {
				delete(c.partitions, ns)
			}
		} else {
			nsPartitions[oldID] = p
		}
		c.Add([]Labeled{o})
	}
}

// Remove objects from the EqC. An object's current Partition is derived
// from its own Namespace/Labels, so this assumes the object's labels
// haven't changed since it was Added; a relabel should be a Remove of the
// old value followed by an Add of the new one.
func (c *EqC) Remove(objects []Labeled) {
	for _, o := range objects {
		ns := o.Namespace()
		nsPartitions, ok := c.partitions[ns]
		if !ok {
			continue
		}

		proj := o.Labels().Project(c.relevant)
		id, ok := c.findLabelSetID(proj)
		if !ok {
			continue
		}

		p, ok := nsPartitions[id]
		if !ok {
			continue
		}
		p.objects = slices.DeleteFunc(p.objects, func(other Labeled) bool {
			return other.ID() == o.ID()
		})

		if len(p.objects) == 0 {
			delete(nsPartitions, id)
			if len(nsPartitions) == 0 {
				delete(c.partitions, ns)
			}
		} else {
			nsPartitions[id] = p
		}
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
	c.partitions = map[NsID]Partitions{}
	c.labeSetIDs = map[LabelSetID]LabelSet{}
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
	for id, existing := range c.labeSetIDs {
		if slices.Equal(existing, ls) {
			return id, true
		}
	}
	return 0, false
}

// internLabelSet returns the LabelSetID for ls's content, allocating a new
// one the first time this exact (relevant-projected) label set is seen.
// Identity is content-addressed, not namespace-scoped: two namespaces with
// the same projected labels share a LabelSetID.
func (c *EqC) internLabelSet(ls LabelSet) LabelSetID {
	if id, ok := c.findLabelSetID(ls); ok {
		return id
	}
	id := LabelSetID(len(c.labeSetIDs))
	c.labeSetIDs[id] = ls
	return id
}
