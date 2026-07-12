package store

import (
	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/tuple"
)

// impl backs the store with a columnar (struct-of-arrays) layout instead of
// a single []tuple.Tuple, so the pointer-bearing and pure-scalar fields are
// separated — see tuple.Columns' doc comment for why that matters at scale.
type impl struct {
	tuples tuple.Columns
}

func (s *impl) Reserve(n int) tuple.Columns {
	start := s.tuples.Len()
	s.tuples = s.tuples.Grow(n)
	return s.tuples.Slice(start, start+n)
}

func (s *impl) AddAll(ts []tuple.Tuple) {
	dst := s.Reserve(len(ts))
	for i, t := range ts {
		dst.Set(i, t)
	}
}

func (s *impl) Len() int {
	return s.tuples.Len()
}

func (s *impl) List() []tuple.Tuple {
	return s.tuples.ToSlice()
}

func (s *impl) Pods() []types.NamespacedName {
	return s.tuples.Pod
}
