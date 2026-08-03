package eqc

import "github.com/bowei/kvec/pkg/kv"

// Classes maintains equivalence classes of labeled objects, grouped by
// namespace.
type Classes struct {
	// byNs maps namespace to perNs information.
	byNs map[string]*perNs
}

type perNs struct {
	// partitions map from KV.Hash() to EqClass for that hash. This should
	// almost always be len == 1 (no collison).
	partitions map[uint64][]Class
}

type Class struct {
	kv *kv.KV
	ns string

	// objs associated with this EqClass. key is the name
	objs map[string]struct{}
}

func (cl *Classes) Get(ns, name string) (*Class, bool)
func (cl *Classes) Add(ns, name string, kv *kv.KV)
func (cl *Classes) Update(ns, name string, kv *kv.KV)
func (cl *Classes) Delete(ns, name string)
