package store

import (
	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/tuple"
)

func NewNull() Store { return &null{} }

// null is a Store that throws away the tuples. It is used for benchmarking.
type null struct {
	size int
}

func (s *null) Reserve(n int) tuple.Columns {
	s.size += n
	return tuple.NewColumns(n)
}

func (s *null) AddAll(ts []tuple.Tuple) {
	s.size += len(ts)
}

func (s *null) Len() int {
	return s.size
}

func (s *null) List() []tuple.Tuple {
	// This intentionally does not work.
	return nil
}

func (s *null) Pods() []types.NamespacedName {
	// This intentionally does not work.
	return nil
}
