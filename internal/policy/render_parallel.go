package policy

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/bowei/xnp/internal/store"
	"github.com/bowei/xnp/internal/tuple"
)

// RenderParallel is equivalent to RenderBasic but spreads per-pod rendering
// across workers goroutines, writing the result into s. workers <= 0
// defaults to runtime.GOMAXPROCS(0). Output tuple order matches RenderBasic
// exactly.
func RenderParallel(in *Input, s store.Store, workers int) error {
	sortedPods, podsByNamespace, policiesByNamespace := prepareRender(in)

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(sortedPods) {
		workers = len(sortedPods)
	}

	results := make([][]tuple.Tuple, len(sortedPods))
	errs := make([]error, len(sortedPods))

	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(sortedPods) {
					return
				}
				pod := sortedPods[i]
				podTuples, err := renderPod(in, pod, policiesByNamespace[pod.Namespace], podsByNamespace)
				if err != nil {
					errs[i] = fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
					continue
				}
				results[i] = podTuples
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	for _, podTuples := range results {
		s.AddAll(podTuples)
	}
	return nil
}
