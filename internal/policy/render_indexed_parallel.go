package policy

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/eqc"
	"github.com/bowei/xnp/internal/repr"
	"github.com/bowei/xnp/internal/store"
	"github.com/bowei/xnp/internal/tuple"
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
func RenderIndexedParallel(in *Input, s store.Store, workers int) error {
	st, err := prepareIndexedRender(in)
	if err != nil {
		return err
	}

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	groups, groupIndex := distinctGroups(st.sortedPods, st.podIdentity)

	templates, err := computeGroupTemplatesParallel(st, groups, workers)
	if err != nil {
		return err
	}

	return materializeTuplesParallel(in, st.sortedPods, groupIndex, templates, s, workers)
}

// distinctGroups returns the (namespace, identity) groups sortedPods touch,
// in first-seen order, plus each pod's index into that slice.
func distinctGroups(sortedPods []repr.Pod, podIdentity []eqc.LabelSetID) (groups []groupKey, groupIndex []int) {
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
					errs[i] = fmt.Errorf("namespace %s: %w", st.in.Table.String(key.ns), err)
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
func materializeTuplesParallel(
	in *Input,
	sortedPods []repr.Pod,
	groupIndex []int,
	templates []*groupTemplates,
	s store.Store,
	workers int,
) error {
	if len(sortedPods) == 0 {
		return nil
	}
	if workers > len(sortedPods) {
		workers = len(sortedPods)
	}

	ownIPs := make([][]string, len(sortedPods))
	offsets := make([]int, len(sortedPods)+1)
	for i, pod := range sortedPods {
		ips := podIPStrings(pod)
		ownIPs[i] = ips
		n := len(ips)
		if n == 0 {
			n = 1
		}
		tmpl := templates[groupIndex[i]]
		offsets[i+1] = offsets[i] + (len(tmpl.ingress)+len(tmpl.egress))*n
	}

	tuples := s.Reserve(offsets[len(sortedPods)])
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
				tmpl := templates[groupIndex[i]]
				podName := types.NamespacedName{Namespace: in.Table.String(pod.Namespace), Name: pod.Name}

				dst := tuples.Slice(offsets[i], offsets[i+1])
				n := 0
				var err error
				for _, tt := range tmpl.ingress {
					var written int
					written, err = writeTuples(dst.Slice(n, dst.Len()), podName, ownIPs[i], tuple.DirectionIngress, tt.verdict, tt.peer, tt.port)
					if err != nil {
						break
					}
					n += written
				}
				if err == nil {
					for _, tt := range tmpl.egress {
						var written int
						written, err = writeTuples(dst.Slice(n, dst.Len()), podName, ownIPs[i], tuple.DirectionEgress, tt.verdict, tt.peer, tt.port)
						if err != nil {
							break
						}
						n += written
					}
				}
				if err != nil {
					errs[i] = fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
				}
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
