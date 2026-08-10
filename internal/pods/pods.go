package pods

import (
	"sync"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/rlabels"
	"k8s.io/utils/set"
)

type Store struct {
	lock sync.RWMutex

	// rlabels is the relevant labels used to create the equivalence classes.
	// It is per namespace: a pod is projected onto the keys some policy names
	// in the pod's own namespace, and never onto another namespace's keys.
	rlabels *rlabels.RLabels

	// pods maps ns -> name -> entry.
	pods map[string]map[string]*Entry
	// eqcToPod maps ns -> eqcToPod -> name -> entry.
	eqcToPod map[string]map[kv.MapKey]map[string]*Entry
	// eqc maps ns -> eqc classes for the namespace.
	eqc map[string]map[kv.MapKey]*kv.KV
	// nodes maps node.Name -> [eqc]
	nodes map[string]set.Set[kv.MapKey]
}

func New() *Store {
	return &Store{
		// An empty RLabels rather than nil, so that an update arriving before
		// Init diffs against "nothing is relevant" instead of dereferencing a
		// nil store.
		rlabels:  &rlabels.RLabels{},
		pods:     map[string]map[string]*Entry{},
		eqcToPod: map[string]map[kv.MapKey]map[string]*Entry{},
		eqc:      map[string]map[kv.MapKey]*kv.KV{},
		nodes:    map[string]set.Set[kv.MapKey]{},
	}
}

// Classes returns an iterator over the equivalence classes of ns, and yields
// nothing at all for a namespace holding no pods. Order is unspecified and the
// store must not be modified during iteration.
//
// A class is only identified within its namespace: two namespaces can hold pods
// whose projections are equal, and they are still different sets of pods. That
// is why this is asked one namespace at a time rather than handing out the
// whole index.
//
// The projections are handed out rather than copied. A kv.KV is immutable once
// built, and this is the one every pod of the class shares -- which is the
// point of keeping the class index at all, since matching a selector against it
// answers for every pod in the class at once.
//
// The read lock is held for the whole walk; see NPolicies.All on what that
// means for a body that reaches into another store.
func (pods *Store) Classes(ns string) func(yield func(*kv.KV) bool) {
	return func(yield func(*kv.KV) bool) {
		pods.lock.RLock()
		defer pods.lock.RUnlock()

		for _, class := range pods.eqc[ns] {
			if !yield(class) {
				return
			}
		}
	}
}
