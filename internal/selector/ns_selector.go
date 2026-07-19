package selector

import (
	"sort"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/labels"
	"github.com/bowei/xnp/internal/repr"
)

// NamespaceSelectorCache memoizes NamespaceSelector matches against the
// (always small) namespace set, keyed by the selector's canonical content,
// so an identical NamespaceSelector reused across many rules is only
// evaluated once.
//
// The match cache is lazily populated; mu guards it for concurrent callers.
type NamespaceSelectorCache struct {
	tbl        *intern.Table
	namespaces []labels.Set
	names      []intern.ID

	mu    sync.RWMutex
	cache map[string][]intern.ID
}

func NewNamespaceSelectorCache(namespaces []repr.Namespace, tbl *intern.Table) *NamespaceSelectorCache {
	c := &NamespaceSelectorCache{tbl: tbl, cache: map[string][]intern.ID{}}
	for _, ns := range namespaces {
		c.namespaces = append(c.namespaces, ns.Labels)
		c.names = append(c.names, ns.Name)
	}
	return c
}

// Match returns the interned names of every namespace selected by sel,
// sorted by name for determinism.
func (c *NamespaceSelectorCache) Match(sel metav1.LabelSelector) ([]intern.ID, error) {
	key := canonicalKey(sel)

	c.mu.RLock()
	names, ok := c.cache[key]
	c.mu.RUnlock()
	if ok {
		return names, nil
	}

	cs, err := CompileSelector(sel, c.tbl)
	if err != nil {
		return nil, err
	}
	var matched []intern.ID
	for i, ls := range c.namespaces {
		if cs.Matches(ls) {
			matched = append(matched, c.names[i])
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return c.tbl.String(matched[i]) < c.tbl.String(matched[j])
	})

	c.mu.Lock()
	c.cache[key] = matched
	c.mu.Unlock()
	return matched, nil
}
