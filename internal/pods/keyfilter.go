package pods

import (
	"github.com/bowei/kvec/pkg/kv"
)

func newKeyFilter() *keyFilter {
	return &keyFilter{
		count: map[string]int{},
	}
}

// keyFilter is a refcounted set of keys that can be used to check if a key
// is present in a set of objects.
type keyFilter struct {
	dk    *kv.DK
	count map[string]int
}

func (kf *keyFilter) has(key string) bool {
	// TODO: use dk to optimize
	c, ok := kf.count[key]
	if !ok {
		return false
	}
	return c > 0
}

func (kf *keyFilter) add(keys []string) {
	for _, k := range keys {
		kf.count[k]++
	}
}

func (kf *keyFilter) remove(keys []string) {
	for _, k := range keys {
		c := kf.count[k]
		if c <= 1 {
			delete(kf.count, k)
		} else {
			kf.count[k]--
		}
	}
}
