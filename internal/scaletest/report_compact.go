package scaletest

import (
	"fmt"
	"strings"
	"time"

	"github.com/bowei/xnp/internal/compact"
)

// flatTupleBytes is the assumed per-tuple wire size for the uncompressed
// baseline CompactReport compares against: unsafe.Sizeof(tuple.Tuple{}) on
// a 64-bit platform (a measured constant, not computed here, to avoid an
// unsafe import just for it).
const flatTupleBytes = 88

// CompactReport summarizes a policy.RenderCompact output: the template
// dictionary size, node/pod chunking, and how its estimated wire size
// compares to the equivalent flat tuple output — see internal/compact's
// package doc for what's being deduplicated and why.
type CompactReport struct {
	Namespaces, Nodes, Pods, Policies int

	Templates  int
	NodeChunks int
	Tuples     int // equivalent flat tuple count, for comparison

	TemplateBytes int
	PodTableBytes int
	FlatBytes     int // Tuples * flatTupleBytes

	GenerateDuration time.Duration
	ConvertDuration  time.Duration
	RenderDuration   time.Duration

	Mem MemStats
}

// SummarizeCompact computes a CompactReport from an already-generated
// TestCase and the compact.Output policy.RenderCompact produced from it.
func SummarizeCompact(tc *TestCase, out *compact.Output, genDur, convDur, renderDur time.Duration, mem MemStats) *CompactReport {
	nodes := map[string]bool{}
	for _, pod := range tc.Pods {
		nodes[pod.Spec.NodeName] = true
	}

	templateBytes, podTableBytes := out.EstimatedSize()
	tuples := out.TupleCount()

	return &CompactReport{
		Namespaces:       len(tc.Namespaces),
		Nodes:            len(nodes),
		Pods:             len(tc.Pods),
		Policies:         len(tc.Policies),
		Templates:        len(out.Templates),
		NodeChunks:       len(out.Nodes),
		Tuples:           tuples,
		TemplateBytes:    templateBytes,
		PodTableBytes:    podTableBytes,
		FlatBytes:        tuples * flatTupleBytes,
		GenerateDuration: genDur,
		ConvertDuration:  convDur,
		RenderDuration:   renderDur,
		Mem:              mem,
	}
}

// String renders r as a plain-text summary table.
func (r *CompactReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "namespaces:  %d\n", r.Namespaces)
	fmt.Fprintf(&b, "nodes:       %d\n", r.Nodes)
	fmt.Fprintf(&b, "pods:        %d\n", r.Pods)
	fmt.Fprintf(&b, "policies:    %d\n", r.Policies)
	fmt.Fprintf(&b, "templates:   %d\n", r.Templates)
	fmt.Fprintf(&b, "node chunks: %d\n", r.NodeChunks)
	fmt.Fprintf(&b, "tuples (flat-equivalent): %d\n", r.Tuples)

	compactBytes := r.TemplateBytes + r.PodTableBytes
	fmt.Fprintf(&b, "flat size (est):    %s\n", formatBytes(uint64(r.FlatBytes)))
	fmt.Fprintf(&b, "compact size (est): %s  (templates=%s pod-table=%s)\n",
		formatBytes(uint64(compactBytes)), formatBytes(uint64(r.TemplateBytes)), formatBytes(uint64(r.PodTableBytes)))
	if compactBytes > 0 {
		fmt.Fprintf(&b, "compression ratio:  %.1fx\n", float64(r.FlatBytes)/float64(compactBytes))
	}

	fmt.Fprintf(&b, "generate:    %s\n", r.GenerateDuration)
	fmt.Fprintf(&b, "convert:     %s\n", r.ConvertDuration)
	fmt.Fprintf(&b, "render:      %s\n", r.RenderDuration)
	fmt.Fprintf(&b, "heap alloc:  start=%s min=%s max=%s end=%s\n",
		formatBytes(r.Mem.Start), formatBytes(r.Mem.Min), formatBytes(r.Mem.Max), formatBytes(r.Mem.End))
	return b.String()
}
