package eqc

import (
	"math"

	"github.com/bowei/xnp/internal/intern"
)

// Later improvement: change to RoaringSets

// NsID is the unique integer for a Namespace.
type NsID = intern.ID

// KeyID is the unique integer for a given label key.
type KeyID = intern.ID

// ValueID is the unique integer for a given label value.
type ValueID = intern.ID

const (
	NoNS NsID = math.MaxUint32
)

type LabelSetID uint64
