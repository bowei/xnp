package npolicies

import (
	"sync"

	"github.com/bowei/xnp/internal/rlabels"
)

type NPolicies struct {
	lock sync.RWMutex

	// nps maps ns -> name -> entry.
	nps map[string]map[string]*entry

	// rlabels is the current set of relevant labels computed from across all
	// of the network policies.
	rlabels *rlabels.RLabels
}

func NewNPolicies() *NPolicies {
	return &NPolicies{
		nps: map[string]map[string]*entry{},
	}
}
