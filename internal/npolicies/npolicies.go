package npolicies

import (
	"sync"

	"github.com/bowei/xnp/internal/rlabels"
)

type Store struct {
	lock sync.RWMutex

	// nps maps ns -> name -> entry.
	nps map[string]map[string]*Entry

	// rlabels is the current set of relevant labels computed from across all
	// of the network policies.
	rlabels *rlabels.RLabels
}

func New() *Store {
	return &Store{
		nps: map[string]map[string]*Entry{},
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

// All iterates every stored policy. Order is unspecified -- the store is keyed
// by namespace and then name, and neither map is walked in any order a caller
// can rely on -- and the store must not be modified during iteration.
//
// The Entries are handed out rather than copied: an Entry is immutable once
// newEntry has built it, and what a consumer wants is the identity of the
// policy a row of its own resolves back to, not a snapshot of the fields.
//
// The read lock is held for the whole walk, so a body that reaches into
// another store puts an ordering on the two locks. Callers that need both
// should collect from here first.
func (nps *Store) All(yield func(*Entry) bool) {
	nps.lock.RLock()
	defer nps.lock.RUnlock()

	for _, inNs := range nps.nps {
		for _, e := range inNs {
			if !yield(e) {
				return
			}
		}
	}
}
