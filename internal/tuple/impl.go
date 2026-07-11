package tuple

// Store holds a rendered Tuple set.
type storeImpl struct {
	tuples []Tuple
}

// NewStore returns an empty Store, sized for capacityHint tuples up front.
// capacityHint may be 0 (grown incrementally via AddAll) or the exact final
// size (immediately followed by a single Reserve of that size).
func NewStore(capacityHint int) Store {
	return &storeImpl{tuples: make([]Tuple, 0, capacityHint)}
}

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
func (s *storeImpl) Reserve(n int) []Tuple {
	start := len(s.tuples)
	s.tuples = append(s.tuples, make([]Tuple, n)...)
	return s.tuples[start : start+n]
}

// AddAll appends ts to the store, in order.
func (s *storeImpl) AddAll(ts []Tuple) {
	copy(s.Reserve(len(ts)), ts)
}

// Len returns the number of tuples in the store.
func (s *storeImpl) Len() int {
	return len(s.tuples)
}

// List returns the store's tuples. The returned slice is the store's live
// backing array, not a copy — callers must not mutate it.
func (s *storeImpl) List() []Tuple {
	return s.tuples
}
