package store

import (
	"github.com/bowei/xnp/internal/repr"
	corev1 "k8s.io/api/core/v1"
)

// Namespaces is the controller's view of the cluster's Namespaces, converted to
// repr and indexed by name.
//
// Unlike Pods there are no equivalence classes here (doc/workflow.tex §Startup
// leaves that step undone): a namespace's labels are compiled whole rather than
// projected onto the relevant keys, and there are few enough namespaces that
// collapsing the identical ones saves little.
//
// Namespaces is not safe for concurrent use.
type Namespaces struct {
	byName map[string]*repr.Namespace
}

func NewNamespaces() *Namespaces {
	return &Namespaces{byName: map[string]*repr.Namespace{}}
}

// Init is the startup path (doc/workflow.tex §Startup). It replaces whatever
// the store currently holds, so the caller hands over a synced Namespace cache
// and incremental updates take over from there.
func (nss *Namespaces) Init(
	nsSeq func(yield func(*corev1.Namespace) bool),
) error {
	byName := map[string]*repr.Namespace{}
	for ns := range nsSeq {
		converted := repr.NewNamespace(ns)
		byName[converted.Name] = &converted
	}

	nss.byName = byName
	return nil
}

// Get returns the named namespace, and whether it is stored at all.
func (nss *Namespaces) Get(name string) (*repr.Namespace, bool) {
	ns, ok := nss.byName[name]
	return ns, ok
}

// Len is the number of namespaces stored.
func (nss *Namespaces) Len() int { return len(nss.byName) }

// All returns an iterator over every stored namespace. The order is
// unspecified. The store must not be modified during iteration.
func (nss *Namespaces) All() func(yield func(*repr.Namespace) bool) {
	return func(yield func(*repr.Namespace) bool) {
		for _, ns := range nss.byName {
			if !yield(ns) {
				return
			}
		}
	}
}
