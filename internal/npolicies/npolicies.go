package npolicies

import (
	"sync"

	"github.com/bowei/xnp/internal/rlabels"
)

type Store struct {
	lock sync.RWMutex

	// nps maps ns -> name -> entry.
	nps map[string]map[string]*entry

	// rlabels is the current set of relevant labels computed from across all
	// of the network policies.
	rlabels *rlabels.RLabels
}

func New() *Store {
	return &Store{
		nps: map[string]map[string]*entry{},
	}
}

// RLabels returns the relevant labels computed across every stored policy, the
// input Pods.Init and Pods.UpdateRLabels are driven by.
//
// The RLabels is handed out rather than copied: it is immutable once built, and
// the pod store keeps the one it is given as the old side of the next
// Difference. What that costs is an ordering rule -- a caller reading this and
// then feeding it to the pod store is reading a snapshot, so a policy update
// landing in between leaves the pod store grouped on the previous key set until
// the update's own UpdateRLabels runs.
func (nps *Store) RLabels() *rlabels.RLabels {
	nps.lock.RLock()
	defer nps.lock.RUnlock()

	return nps.rlabels
}
