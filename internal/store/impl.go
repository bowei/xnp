package store

import "github.com/bowei/xnp/internal/tuple"

type impl struct {
	tuples []tuple.Tuple
}

func (s *impl) Reserve(n int) []tuple.Tuple {
	start := len(s.tuples)
	s.tuples = append(s.tuples, make([]tuple.Tuple, n)...)
	return s.tuples[start : start+n]
}

func (s *impl) AddAll(ts []tuple.Tuple) {
	copy(s.Reserve(len(ts)), ts)
}

func (s *impl) Len() int {
	return len(s.tuples)
}

func (s *impl) List() []tuple.Tuple {
	return s.tuples
}
