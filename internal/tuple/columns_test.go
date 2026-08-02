package tuple

import (
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestDiscardColumns_Len(t *testing.T) {
	c := NewDiscardColumns(42)
	if got := c.Len(); got != 42 {
		t.Errorf("Len() = %d, want 42", got)
	}
}

func TestDiscardColumns_SliceMaterializesReal(t *testing.T) {
	c := NewDiscardColumns(10)
	sub := c.Slice(2, 5)

	if got := sub.Len(); got != 3 {
		t.Fatalf("sub.Len() = %d, want 3", got)
	}

	pod := types.NamespacedName{Namespace: "ns", Name: "pod"}
	sub.Set(0, Tuple{Pod: pod, Verdict: VerdictAllow})
	if got := sub.At(0).Pod; got != pod {
		t.Errorf("At(0).Pod = %v, want %v", got, pod)
	}

	// A nested Slice on the now-real Columns behaves like a normal Columns.
	nested := sub.Slice(1, sub.Len())
	if got := nested.Len(); got != 2 {
		t.Errorf("nested.Len() = %d, want 2", got)
	}
}

func TestDiscardColumns_SliceOutOfRangePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic slicing past virtualLen")
		}
	}()
	NewDiscardColumns(5).Slice(0, 6)
}

func TestDiscardColumns_SetPanicsBeforeSlicing(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic calling Set on a discard Columns")
		}
	}()
	NewDiscardColumns(5).Set(0, Tuple{})
}

func TestDiscardColumns_AtPanicsBeforeSlicing(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic calling At on a discard Columns")
		}
	}()
	NewDiscardColumns(5).At(0)
}

func TestDiscardColumns_GrowPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic calling Grow on a discard Columns")
		}
	}()
	NewDiscardColumns(5).Grow(1)
}

// TestDiscardColumns_ConcurrentSliceIsRaceSafe mirrors how
// RenderIndexedParallel uses a Store: one Reserve for the whole run, then
// many goroutines each Slice their own disjoint chunk and write into it.
// Each Slice on a discard Columns must allocate independently, or these
// writes would race on shared memory.
func TestDiscardColumns_ConcurrentSliceIsRaceSafe(t *testing.T) {
	const workers = 8
	c := NewDiscardColumns(workers * 3)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			dst := c.Slice(w*3, w*3+3)
			for i := 0; i < dst.Len(); i++ {
				dst.Set(i, Tuple{Pod: types.NamespacedName{Name: "pod"}, Verdict: VerdictAllow})
			}
		}(w)
	}
	wg.Wait()
}
