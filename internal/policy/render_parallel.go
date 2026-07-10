package policy

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// RenderParallel is equivalent to RenderBasic but spreads per-pod rendering
// across workers goroutines. workers <= 0 defaults to runtime.GOMAXPROCS(0).
// Output tuple order matches RenderBasic exactly.
func RenderParallel(policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace, workers int) ([]Tuple, error) {
	sortedPods, podsByNamespace, policiesByNamespace := prepareRender(policies, pods)

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(sortedPods) {
		workers = len(sortedPods)
	}

	results := make([][]Tuple, len(sortedPods))
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
				podTuples, err := renderPod(pod, policiesByNamespace[pod.Namespace], podsByNamespace, namespaces)
				if err != nil {
					errs[i] = fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
					continue
				}
				results[i] = podTuples
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	var tuples []Tuple
	for _, podTuples := range results {
		tuples = append(tuples, podTuples...)
	}
	return tuples, nil
}
