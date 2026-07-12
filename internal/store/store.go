package store

import (
	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/tuple"
)

// Store holds a rendered Tuple set.
type Store interface {
	// Reserve grows the store by n elements and returns a columnar view
	// directly into the store's backing arrays for that new region, for
	// callers that write results in by index rather than by appending one at
	// a time.
	//
	// Reserve itself must only ever be called by one goroutine at a time, and
	// only before any concurrent writes into the region it returns begin — it is
	// not a per-writer allocator. Once it returns, separate goroutines may write
	// to disjoint index ranges of the result concurrently without further
	// synchronization, the same discipline the indexed renderers already use
	// over a bare slice.
	Reserve(n int) tuple.Columns

	// AddAll appends ts to the store, in order.
	AddAll(ts []tuple.Tuple)

	// Len returns the number of tuples in the store.
	Len() int

	// List returns the store's tuples. Since the store's backing storage is
	// columnar, this materializes a fresh []tuple.Tuple (re-introducing a
	// Port/EndPort pointer allocation per non-wildcard tuple) — fine for
	// tests and small stores, but callers that only need one field at scale
	// (e.g. reporting per-pod tuple counts) should prefer Pods instead of
	// paying for a full materialization just to read it back out.
	List() []tuple.Tuple

	// Pods returns the store's Pod column directly, with no per-tuple
	// materialization.
	Pods() []types.NamespacedName
}

// NewStore returns an empty Store, sized for capacityHint tuples up front.
// capacityHint may be 0 (grown incrementally via AddAll) or the exact final
// size (immediately followed by a single Reserve of that size).
func NewStore(capacityHint int) Store {
	return &impl{tuples: tuple.NewColumnsCap(capacityHint)}
}
