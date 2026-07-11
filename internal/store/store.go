package store

import "github.com/bowei/xnp/internal/tuple"

// Store holds a rendered Tuple set.
type Store interface {
	// Reserve grows the store by n elements and returns a slice view directly
	// into the store's backing array for that new region, for callers that write
	// results in by index rather than by appending one at a time.
	//
	// Reserve itself must only ever be called by one goroutine at a time, and
	// only before any concurrent writes into the region it returns begin — it is
	// not a per-writer allocator. Once it returns, separate goroutines may write
	// to disjoint index ranges of the result concurrently without further
	// synchronization, the same discipline the indexed renderers already use
	// over a bare slice.
	Reserve(n int) []tuple.Tuple

	// AddAll appends ts to the store, in order.
	AddAll(ts []tuple.Tuple)

	// Len returns the number of tuples in the store.
	Len() int

	// List returns the store's tuples.
	List() []tuple.Tuple
}

// NewStore returns an empty Store, sized for capacityHint tuples up front.
// capacityHint may be 0 (grown incrementally via AddAll) or the exact final
// size (immediately followed by a single Reserve of that size).
func NewStore(capacityHint int) Store {
	return &impl{tuples: make([]tuple.Tuple, 0, capacityHint)}
}
