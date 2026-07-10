package policy

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
)

// RenderIndexedParallel is RenderIndexed spread across worker goroutines.
// workers <= 0 defaults to runtime.GOMAXPROCS(0). Output tuple order matches
// RenderIndexed exactly.
//
// Parallelizing at pod granularity, the way RenderParallel does for
// RenderBasic, would undercut RenderIndexed's whole point: most pods share a
// (namespace, identity) group, so per-pod workers would mostly race each
// other to recompute the same handful of group templates (or require
// locking groupCache). Instead this splits into two fan-out phases, each
// sized to the work that's actually there:
//
//  1. One goroutine pool computes ingress/egress templates once per distinct
//     (namespace, identity) group — the selector-heavy part RenderIndexed's
//     identity classes already dedup'd down to a small set.
//  2. A second pool walks every pod and materializes its tuples from the
//     now-shared, read-only template slice — the fan-out-heavy part, since
//     concrete pod addresses still make output size proportional to pod
//     count even after templates are shared.
func RenderIndexedParallel(policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace, workers int) ([]Tuple, error) {
	st, err := prepareIndexedRender(policies, pods, namespaces)
	if err != nil {
		return nil, err
	}

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	groups, groupIndex := distinctGroups(st.sortedPods, st.podIdentity)

	templates, err := computeGroupTemplatesParallel(st, groups, workers)
	if err != nil {
		return nil, err
	}

	return materializeTuplesParallel(st.sortedPods, groupIndex, templates, workers)
}

// distinctGroups returns the (namespace, identity) groups sortedPods touch,
// in first-seen order, plus each pod's index into that slice.
func distinctGroups(sortedPods []*corev1.Pod, podIdentity []LabelSetID) (groups []groupKey, groupIndex []int) {
	seen := map[groupKey]int{}
	groupIndex = make([]int, len(sortedPods))
	for i, pod := range sortedPods {
		key := groupKey{pod.Namespace, podIdentity[i]}
		idx, ok := seen[key]
		if !ok {
			idx = len(groups)
			seen[key] = idx
			groups = append(groups, key)
		}
		groupIndex[i] = idx
	}
	return groups, groupIndex
}

// computeGroupTemplatesParallel resolves every group in groups exactly once,
// fanned out across workers goroutines pulling from a shared counter.
func computeGroupTemplatesParallel(st *indexedState, groups []groupKey, workers int) ([]*groupTemplates, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	if workers > len(groups) {
		workers = len(groups)
	}

	templates := make([]*groupTemplates, len(groups))
	errs := make([]error, len(groups))

	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(groups) {
					return
				}
				key := groups[i]
				tmpl, err := st.groupTemplatesFor(key)
				if err != nil {
					errs[i] = fmt.Errorf("namespace %s: %w", key.ns, err)
					continue
				}
				templates[i] = tmpl
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return templates, nil
}

// materializeTuplesParallel turns each pod's precomputed group template into
// concrete tuples, fanned out across workers goroutines. templates is only
// read here, never written, so concurrent access needs no locking.
//
// Each pod's tuple count is knowable from its template alone (ingress +
// egress entry count × own-IP count), so offsets are computed once up
// front and every worker writes straight into its pod's disjoint slice of a
// single preallocated result — as opposed to each worker building its own
// slice and a final pass concatenating all of them, which briefly doubles
// peak memory (both copies of ~25M tuples live at once) and, at that size,
// loses more to the resulting GC pressure than the parallel fan-out saves.
func materializeTuplesParallel(sortedPods []*corev1.Pod, groupIndex []int, templates []*groupTemplates, workers int) ([]Tuple, error) {
	if len(sortedPods) == 0 {
		return nil, nil
	}
	if workers > len(sortedPods) {
		workers = len(sortedPods)
	}

	ownIPs := make([][]string, len(sortedPods))
	offsets := make([]int, len(sortedPods)+1)
	for i, pod := range sortedPods {
		ips := podIPs(pod)
		ownIPs[i] = ips
		n := len(ips)
		if n == 0 {
			n = 1
		}
		tmpl := templates[groupIndex[i]]
		offsets[i+1] = offsets[i] + (len(tmpl.ingress)+len(tmpl.egress))*n
	}

	tuples := make([]Tuple, offsets[len(sortedPods)])

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
				tmpl := templates[groupIndex[i]]
				podName := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}

				dst := tuples[offsets[i]:offsets[i+1]]
				n := 0
				for _, tt := range tmpl.ingress {
					n += copy(dst[n:], makeTuples(podName, ownIPs[i], DirectionIngress, tt.verdict, tt.peer, tt.port))
				}
				for _, tt := range tmpl.egress {
					n += copy(dst[n:], makeTuples(podName, ownIPs[i], DirectionEgress, tt.verdict, tt.peer, tt.port))
				}
			}
		}()
	}
	wg.Wait()

	return tuples, nil
}
