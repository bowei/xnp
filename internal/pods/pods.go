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
	// eqc maps ns -> eqc -> name -> entry.
	eqc map[string]map[kv.MapKey]map[string]*Entry
	// nodes maps node.Name -> [eqc]
	nodes map[string]set.Set[kv.MapKey]
}

func New() *Store {
	return &Store{
		// An empty RLabels rather than nil, so that an update arriving before
		// Init diffs against "nothing is relevant" instead of dereferencing a
		// nil store.
		rlabels: &rlabels.RLabels{},
		pods:    map[string]map[string]*Entry{},
		eqc:     map[string]map[kv.MapKey]map[string]*Entry{},
		nodes:   map[string]set.Set[kv.MapKey]{},
	}
}
