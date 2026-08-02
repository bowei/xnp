package tuple

import "k8s.io/apimachinery/pkg/types"

// Columns is a struct-of-arrays view over a contiguous run of Tuples. It
// exists so a Store's backing storage can separate the pointer-bearing
// fields (Pod, Src, Dest — rescanned by the GC every mark cycle) from the
// pure-scalar ones (Port/EndPort/Protocol/Direction/Verdict), which the GC's
// pointer bitmap lets it skip over entirely. At the tens-of-millions-of-rows
// scale this renders, that shrinks both live-heap size and per-cycle GC scan
// cost versus a single []Tuple.
//
// A Columns value shares its backing arrays with whatever it was sliced
// from; Slice, Set, and At operate by index like a normal slice.
type Columns struct {
	Pod  []types.NamespacedName
	Src  []string
	Dest []string

	// Port/EndPort are only meaningful where the corresponding Has* entry is
	// true; unlike Tuple's *int32 fields, "unset" is a bool flag rather than
	// a nil pointer, so these columns carry no GC-scanned pointers at all.
	Port       []int32
	EndPort    []int32
	HasPort    []bool
	HasEndPort []bool

	Protocol  []Protocol
	Direction []Direction
	Verdict   []Verdict

	// discard marks a Columns returned by NewDiscardColumns: it has no
	// backing arrays yet, only a virtualLen. See NewDiscardColumns.
	discard    bool
	virtualLen int
}

// NewDiscardColumns returns a placeholder Columns of virtual length n with
// no backing arrays allocated. It exists for a Store that computes tuples
// but never retains them (store.NewNull, used for benchmarking): Reserve
// still has to hand back something of the right size to satisfy callers
// that write straight into a preallocated result instead of appending
// (RenderIndexed, RenderIndexedParallel — see their doc comments), but
// paying for a real n-sized allocation up front, only to discard it,
// defeats the point of isolating render-compute cost from result-storage
// cost.
//
// Instead, Slice on a discard Columns lazily allocates a real (small)
// Columns sized to just the requested sub-range, the same per-chunk-cost
// shape RenderBasic/RenderParallel already pay via makeTuples — so total
// allocation scales with however finely a caller slices, never with n
// itself. Reading from a discard Columns before it's been sliced down to a
// real one is a bug in the caller, so Set/At/ToSlice/Grow panic on it
// rather than silently indexing into nothing.
func NewDiscardColumns(n int) Columns {
	return Columns{discard: true, virtualLen: n}
}

// NewColumns allocates a Columns with n rows, all zero-valued.
func NewColumns(n int) Columns {
	return Columns{
		Pod:  make([]types.NamespacedName, n),
		Src:  make([]string, n),
		Dest: make([]string, n),

		Port:       make([]int32, n),
		EndPort:    make([]int32, n),
		HasPort:    make([]bool, n),
		HasEndPort: make([]bool, n),

		Protocol:  make([]Protocol, n),
		Direction: make([]Direction, n),
		Verdict:   make([]Verdict, n),
	}
}

// NewColumnsCap returns a zero-length Columns with capacity for n rows in
// each column, so a subsequent Grow up to n rows doesn't reallocate.
func NewColumnsCap(n int) Columns {
	return Columns{
		Pod:  make([]types.NamespacedName, 0, n),
		Src:  make([]string, 0, n),
		Dest: make([]string, 0, n),

		Port:       make([]int32, 0, n),
		EndPort:    make([]int32, 0, n),
		HasPort:    make([]bool, 0, n),
		HasEndPort: make([]bool, 0, n),

		Protocol:  make([]Protocol, 0, n),
		Direction: make([]Direction, 0, n),
		Verdict:   make([]Verdict, 0, n),
	}
}

// Len returns the number of rows.
func (c Columns) Len() int {
	if c.discard {
		return c.virtualLen
	}
	return len(c.Pod)
}

// Grow extends c by n rows, appended at the end, and returns the result.
// Like a plain append(s, make([]T, n)...) on a single slice, the compiler
// elides the intermediate allocation for each column here, and repeated
// Grow calls get append's amortized-O(1) geometric capacity growth rather
// than reallocating to an exact size every time.
func (c Columns) Grow(n int) Columns {
	if c.discard {
		panic("tuple: Grow called on a discard Columns")
	}
	return Columns{
		Pod:  append(c.Pod, make([]types.NamespacedName, n)...),
		Src:  append(c.Src, make([]string, n)...),
		Dest: append(c.Dest, make([]string, n)...),

		Port:       append(c.Port, make([]int32, n)...),
		EndPort:    append(c.EndPort, make([]int32, n)...),
		HasPort:    append(c.HasPort, make([]bool, n)...),
		HasEndPort: append(c.HasEndPort, make([]bool, n)...),

		Protocol:  append(c.Protocol, make([]Protocol, n)...),
		Direction: append(c.Direction, make([]Direction, n)...),
		Verdict:   append(c.Verdict, make([]Verdict, n)...),
	}
}

// Slice returns the sub-view [start:end), sharing the same backing arrays
// as c (writes through it are visible in c and vice versa).
//
// On a discard Columns (see NewDiscardColumns), Slice instead lazily
// allocates a fresh, real Columns of length end-start and returns that —
// so nested/concurrent Slice calls (as RenderIndexedParallel makes, one per
// pod, from separate goroutines) each get their own independent backing
// arrays rather than racing on shared memory.
func (c Columns) Slice(start, end int) Columns {
	if c.discard {
		if start < 0 || end > c.virtualLen || start > end {
			panic("tuple: Columns.Slice out of range")
		}
		return NewColumns(end - start)
	}
	return Columns{
		Pod:  c.Pod[start:end],
		Src:  c.Src[start:end],
		Dest: c.Dest[start:end],

		Port:       c.Port[start:end],
		EndPort:    c.EndPort[start:end],
		HasPort:    c.HasPort[start:end],
		HasEndPort: c.HasEndPort[start:end],

		Protocol:  c.Protocol[start:end],
		Direction: c.Direction[start:end],
		Verdict:   c.Verdict[start:end],
	}
}

// Set writes t into row i.
func (c Columns) Set(i int, t Tuple) {
	if c.discard {
		panic("tuple: Set called on a discard Columns; Slice it down to a real one first")
	}
	c.Pod[i] = t.Pod
	c.Src[i] = t.Src
	c.Dest[i] = t.Dest

	if t.Port != nil {
		c.Port[i] = *t.Port
		c.HasPort[i] = true
	}
	if t.EndPort != nil {
		c.EndPort[i] = *t.EndPort
		c.HasEndPort[i] = true
	}

	c.Protocol[i] = t.Protocol
	c.Direction[i] = t.Direction
	c.Verdict[i] = t.Verdict
}

// At materializes row i as a Tuple.
func (c Columns) At(i int) Tuple {
	if c.discard {
		panic("tuple: At called on a discard Columns; Slice it down to a real one first")
	}
	t := Tuple{
		Pod:       c.Pod[i],
		Src:       c.Src[i],
		Dest:      c.Dest[i],
		Protocol:  c.Protocol[i],
		Direction: c.Direction[i],
		Verdict:   c.Verdict[i],
	}
	if c.HasPort[i] {
		p := c.Port[i]
		t.Port = &p
	}
	if c.HasEndPort[i] {
		p := c.EndPort[i]
		t.EndPort = &p
	}
	return t
}

// ToSlice materializes every row as a []Tuple. Intended for callers outside
// the hot render path (tests, reporting) that need the struct form; it
// re-introduces the per-row Port/EndPort pointer allocations Columns avoids,
// so it isn't meant to be called on the hot path.
func (c Columns) ToSlice() []Tuple {
	out := make([]Tuple, c.Len())
	for i := range out {
		out[i] = c.At(i)
	}
	return out
}
