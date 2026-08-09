package pods

import (
	"sync"

	"github.com/bowei/kvec/pkg/kv"
	"k8s.io/utils/set"
)

type Pods struct {
	lock sync.RWMutex

	// rlabels is the set of relevant labels used to create the equivalence classes.
	rlabels set.Set[string]

	// pods maps ns -> name -> entry.
	pods map[string]map[string]*entry
	// eqc maps ns -> eqc -> name -> entry.
	eqc map[string]map[kv.MapKey]map[string]*entry
	// nodes maps node.Name -> [eqc]
	nodes map[string]set.Set[kv.MapKey]
}

func NewPods() *Pods {
	return &Pods{
		pods:  map[string]map[string]*entry{},
		eqc:   map[string]map[kv.MapKey]map[string]*entry{},
		nodes: map[string]set.Set[kv.MapKey]{},
	}
}
