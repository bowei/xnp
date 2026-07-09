package scaletest

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/policy"
)

// Stats summarizes a distribution of per-object tuple counts.
type Stats struct {
	Min, Max int
	Avg      float64
}

// Report summarizes the size of a generated TestCase and the tuple set the
// renderer produced from it, in particular the per-node distribution: that
// is the number the README's lightweight per-node agent actually has to
// carry, and is the thing scale testing is meant to characterize.
type Report struct {
	Namespaces, Nodes, Pods, Policies, Tuples int

	TuplesPerPod  Stats
	TuplesPerNode Stats

	GenerateDuration time.Duration
	RenderDuration   time.Duration
}

// Summarize computes a Report from an already-generated TestCase and the
// Tuples policy.Render produced from it. It is a pure function of its
// arguments (durations are passed in rather than measured internally) so
// it's straightforward to unit test without timing flakiness.
func Summarize(tc *TestCase, tuples []policy.Tuple, genDur, renderDur time.Duration) *Report {
	nodeOf := make(map[types.NamespacedName]string, len(tc.Pods))
	nodes := make(map[string]bool)
	for _, pod := range tc.Pods {
		key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
		nodeOf[key] = pod.Spec.NodeName
		nodes[pod.Spec.NodeName] = true
	}

	tuplesPerPod := make(map[types.NamespacedName]int, len(tc.Pods))
	tuplesPerNode := make(map[string]int, len(nodes))
	for _, t := range tuples {
		tuplesPerPod[t.Pod]++
		tuplesPerNode[nodeOf[t.Pod]]++
	}

	r := &Report{
		Namespaces:       len(tc.Namespaces),
		Nodes:            len(nodes),
		Pods:             len(tc.Pods),
		Policies:         len(tc.Policies),
		Tuples:           len(tuples),
		GenerateDuration: genDur,
		RenderDuration:   renderDur,
	}
	r.TuplesPerPod = statsOf(tuplesPerPod, len(tc.Pods))
	r.TuplesPerNode = statsOf(tuplesPerNode, len(nodes))
	return r
}

// statsOf computes Stats over counts' values. n is the number of objects
// counts was built from; every pod (and thus every node) is guaranteed a
// map entry because policy.Render always emits at least a default-verdict
// tuple per pod/direction, so len(counts) == n.
func statsOf[K comparable](counts map[K]int, n int) Stats {
	if n == 0 {
		return Stats{}
	}
	sum, min, max := 0, 0, 0
	first := true
	for _, c := range counts {
		if first || c < min {
			min = c
		}
		if c > max {
			max = c
		}
		sum += c
		first = false
	}
	return Stats{Min: min, Max: max, Avg: float64(sum) / float64(n)}
}

// String renders r as a plain-text summary table.
func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "namespaces:  %d\n", r.Namespaces)
	fmt.Fprintf(&b, "nodes:       %d\n", r.Nodes)
	fmt.Fprintf(&b, "pods:        %d\n", r.Pods)
	fmt.Fprintf(&b, "policies:    %d\n", r.Policies)
	fmt.Fprintf(&b, "tuples:      %d\n", r.Tuples)
	fmt.Fprintf(&b, "tuples/pod:  min=%d max=%d avg=%.1f\n", r.TuplesPerPod.Min, r.TuplesPerPod.Max, r.TuplesPerPod.Avg)
	fmt.Fprintf(&b, "tuples/node: min=%d max=%d avg=%.1f\n", r.TuplesPerNode.Min, r.TuplesPerNode.Max, r.TuplesPerNode.Avg)
	fmt.Fprintf(&b, "generate:    %s\n", r.GenerateDuration)
	fmt.Fprintf(&b, "render:      %s\n", r.RenderDuration)
	return b.String()
}
