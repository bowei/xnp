package eqc

import (
	"math"

	"github.com/bowei/xnp/internal/intern"
)

// Later improvement: change to RoaringSets

// NsID is the unique integer for a Namespace.
type NsID intern.ID

// KeyID is the unique integer for a given label key.
type KeyID intern.ID

// ValueID is the unique integer for a given label value.
type ValueID intern.ID

const (
	NoNS    NsID    = math.MaxUint32
	NoValue ValueID = math.MaxUint32
)

type LabelSetID uint64

// KeyValue is a key/value label pair, packed into a single comparable,
// hashable, sortable intern.Pair (key in the high bits so sorting by
// KeyValue also sorts by key first).
type KeyValue intern.Pair

func (p KeyValue) K() KeyID   { return KeyID(intern.Pair(p).Key()) }
func (p KeyValue) V() ValueID { return ValueID(intern.Pair(p).Value()) }

// KV makes examples more readable.
func KV(k KeyID, v ValueID) KeyValue {
	return KeyValue(intern.NewPair(intern.ID(k), intern.ID(v)))
}
