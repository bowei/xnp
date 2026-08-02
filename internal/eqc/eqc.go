package eqc

import "github.com/bowei/xnp/internal/labels"

type Labeled interface {
	ID() uint64
	Namespace() NsID
	Labels() labels.Set
}

// EqC are equivalence classes: tracks dividing resources into equivalece
// classes of the same label.
type EqC struct {
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
}

// Add objects to the EqC, splitting the objects based on their
// relevant Labels.
func (c *EqC) Add(objects []Labeled) {

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
}

// Remove objects from the EqC. Each object is located via the ID-keyed
// index recorded at Add time, so this does not depend on the labels
// passed on o still matching what was stored (unlike Update, a stale
// label snapshot on o does not cause a mislocated or skipped removal).
func (c *EqC) Remove(objects []Labeled) {
}

// evict removes the object with the given id at loc from its partition
// in namespace ns, in O(1): swap the last object into loc's slot (fixing
// up the moved object's tracked index) and truncate.
func (c *EqC) evict(ns NsID, id uint64, loc objLoc) {
}

// UpdateRelevant labels.
//
// * Labels that have been removed can cause Partitions to be merged
// * Labels that have been added can cause Partitions to be split.
func (c *EqC) UpdateRelevant(keys []KeyID) {
}

// Range returns an iterator over the NsID and Partitions.
func (c *EqC) Range() func(yield func(NsID, Partitions) bool) {
}
