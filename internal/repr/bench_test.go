package repr

// Conversion benchmarks: how long a full resync's worth of API objects takes
// to turn into repr form, and how much memory the result occupies.
//
// Each case is one batch of N objects converted and retained, at one of three
// object shapes, so the reported figure is the whole batch rather than a
// single call:
//
//	go test ./internal/repr/ -run '^$' -bench 'BenchmarkNew' -benchmem
//
// Read ns/obj for the per-object cost, ns/op for the batch, and B/op for what
// the batch leaves resident. ns/obj is the figure to compare down a column:
// where it climbs with N, the extra is the collector working against a live
// set that no longer fits in cache, which is the part a single-object
// benchmark would not show. The largest cases take seconds per iteration and
// so run only a handful of them; -benchtime=1x makes a full sweep quicker
// without changing those numbers much.
//
// Two things are bounded deliberately, because the honest answer at these
// counts is limited by memory rather than by time:
//
//   - The source objects are generated in chunks of benchChunkBytes and
//     dropped as each chunk is converted, since holding 1M large corev1.Pods
//     costs several GiB before any conversion happens. Every converted object
//     still gets its own distinct source: sharing one would let the converted
//     set share label strings that a real batch would each hold separately,
//     which is exactly the retention being measured. Generating is fmt-bound
//     and runs with the timer stopped. The chunk is big enough that a source
//     is not read out of cache when it is converted, which a small one would
//     make it.
//   - The converted objects are all retained, since staying resident is what
//     repr is for. A case whose result would not fit in -repr.membudget skips
//     itself and reports the estimate instead of driving the machine into swap.
//     Skipped cases print nothing without -v, so run with it to see which
//     sizes did not fit and by how much — for the larger shapes that estimate
//     is the answer, not a caveat on it.
//
// Every converted object has its Original cleared before it is retained, so
// what stays resident is the mirror alone. Keeping the pointer would make each
// case's live set the API object plus the mirror, which is the opposite of the
// question — repr exists so that the API object need not stay in memory. The
// mirror still shares the strings it copied out of the original (a Go string
// copies by header), so what the clearing drops is the API struct itself and
// every field repr did not keep, which is the bulk of it.

import (
	"flag"
	"fmt"
	"runtime"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var benchMemBudget = flag.Float64("repr.membudget", 4,
	"GiB of converted objects a repr benchmark case may hold live before skipping itself")

// benchChunkBytes caps the source objects a case has live at once. See
// benchStream for why sources are streamed in bounded chunks rather than
// generated up front, and the file comment for why the bound is this large.
const benchChunkBytes = 64 << 20

// benchScale is the shape of a generated object, expressed in the dimensions
// repr actually reads: labels, IPs, and selector/rule structure. Fields that
// no converter looks at (container specs, conditions, annotations) are left
// out, since inflating them would cost generation time without changing what
// is measured.
type benchScale struct {
	name string

	// labels is the number of labels on a generated Pod or Namespace.
	labels int
	// dualStack gives a generated Pod an IPv6 address alongside its IPv4 one,
	// doubling the addresses NewPod has to parse.
	dualStack bool

	// The rest shape a generated NetworkPolicy. The policy's own podSelector
	// is built to matchLabels/matchExprs; a peer's selectors are built to
	// peerMatchLabels/peerMatchExprs, which are smaller, because a rule
	// normally names its peer by one or two labels however elaborate the
	// selector picking the policy's own subject is. Sizing both from one pair
	// of numbers would multiply the difference by peers*rules and make the
	// large policy an object no cluster has.
	matchLabels     int
	matchExprs      int
	peerMatchLabels int
	peerMatchExprs  int
	rules           int  // ingress rules, and separately egress rules
	egress          bool // whether egress rules are generated at all
	peers           int  // peers per rule
	ports           int  // ports per rule
}

var benchScales = []benchScale{
	// The floor: one label, and a policy that is one rule allowing one peer on
	// one port. Nothing here is representative, but it isolates the fixed cost
	// of converting an object at all.
	{
		name: "minimal", labels: 1,
		matchLabels: 1, peerMatchLabels: 1,
		rules: 1, peers: 1, ports: 1,
	},
	// A typical workload: the app.kubernetes.io set plus a few local labels,
	// dual-stack, and a policy naming a handful of peers and ports.
	{
		name: "medium", labels: 8, dualStack: true,
		matchLabels: 3, matchExprs: 1, peerMatchLabels: 2,
		rules: 3, egress: true, peers: 3, ports: 3,
	},
	// The tail: mesh/CD/chart labels stacked on top of the workload's own, and
	// a policy written as one long allow-list.
	{
		name: "large", labels: 32, dualStack: true,
		matchLabels: 8, matchExprs: 4, peerMatchLabels: 3, peerMatchExprs: 1,
		rules: 10, egress: true, peers: 8, ports: 8,
	},
}

var benchCounts = []struct {
	name string
	n    int
}{
	{"1k", 1_000},
	{"10k", 10_000},
	{"100k", 100_000},
	{"1M", 1_000_000},
}

// TestBenchGenerators checks that every generated object converts, at both
// ends of the index range the benchmarks walk. The benchmarks would catch this
// themselves, but only when run: a generator that produces an object repr
// rejects (an index that overflows an address, say) should fail in `go test`,
// not hours later under `go test -bench`.
func TestBenchGenerators(t *testing.T) {
	for _, sc := range benchScales {
		t.Run(sc.name, func(t *testing.T) {
			for _, i := range []int{0, 1, benchNamespaces + 1, 1<<16 + 1, benchCounts[len(benchCounts)-1].n - 1} {
				NewNamespace(benchNamespace(sc, i))
				if _, err := NewPod(benchPod(sc, i)); err != nil {
					t.Errorf("NewPod(benchPod(%s, %d)): %v", sc.name, i, err)
				}
				if _, err := NewNetworkPolicy(benchPolicy(sc, i)); err != nil {
					t.Errorf("NewNetworkPolicy(benchPolicy(%s, %d)): %v", sc.name, i, err)
				}
			}
		})
	}
}

// The converters below drop Original as part of the conversion, so that both
// the calibration batch and the measured one hold mirrors only. See the file
// comment for why. The clearing is a single store per object and is inside the
// timer, where it belongs.

func BenchmarkNewNamespace(b *testing.B) {
	benchConvert(b, benchNamespace, func(ns *corev1.Namespace) (Namespace, error) {
		d := NewNamespace(ns)
		d.Original = nil
		return d, nil
	})
}

func BenchmarkNewPod(b *testing.B) {
	benchConvert(b, benchPod, func(pod *corev1.Pod) (Pod, error) {
		d, err := NewPod(pod)
		d.Original = nil
		return d, err
	})
}

func BenchmarkNewNetworkPolicy(b *testing.B) {
	benchConvert(b, benchPolicy, func(np *networkingv1.NetworkPolicy) (*NetworkPolicy, error) {
		d, err := NewNetworkPolicy(np)
		if err != nil {
			return nil, err
		}
		d.Original = nil
		return d, nil
	})
}

// benchConvert runs conv over a batch of every size in benchCounts, at every
// shape in benchScales.
func benchConvert[S, D any](b *testing.B, gen func(sc benchScale, i int) S, conv func(S) (D, error)) {
	for _, sc := range benchScales {
		for _, c := range benchCounts {
			b.Run(sc.name+"/"+c.name, func(b *testing.B) {
				genOne := func(i int) S { return gen(sc, i) }

				perObj, err := liveBytesPerObject(genOne, conv)
				if err != nil {
					b.Fatalf("converting a calibration batch: %v", err)
				}
				if need := perObj * float64(c.n); need > *benchMemBudget*(1<<30) {
					b.Skipf("%s converted objects would hold ~%.1f GiB live, over the %.1f GiB budget (-repr.membudget)",
						c.name, need/(1<<30), *benchMemBudget)
				}

				chunkLen := min(benchChunkLen(genOne), c.n)
				out := make([]D, c.n)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if err := benchStream(b, genOne, conv, out, chunkLen); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()

				// ns/op is the batch; this is the per-object cost the batch
				// figures for the other sizes should be checked against, since
				// a departure from a flat line across sizes is the interesting
				// result (GC pressure rising with the live set).
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(c.n), "ns/obj")
				runtime.KeepAlive(out)
			})
		}
	}
}

// benchStream fills out by converting len(out) distinct generated sources,
// generating them a chunk at a time and dropping each chunk as soon as it is
// converted. Only one chunk of sources is ever live, so a case's resident set
// is its converted objects plus that chunk, rather than a whole second batch
// of API objects that the measurement is not about.
//
// Generation is charged to nobody: b's timer is stopped around it, which
// excludes it from ns/op and from B/op both. It has to be excluded rather than
// amortized, since generating a source is fmt.Sprintf-bound and costs more
// than converting one.
//
// b may be nil, for a caller with no timer to stop.
func benchStream[S, D any](b *testing.B, gen func(i int) S, conv func(S) (D, error), out []D, chunkLen int) error {
	chunk := make([]S, chunkLen)
	for base := 0; base < len(out); base += chunkLen {
		if b != nil {
			b.StopTimer()
		}
		// Released before generating, not after converting: the sources of the
		// chunk just converted stay reachable until here, so that the peak is
		// one chunk rather than two.
		clear(chunk)
		n := min(chunkLen, len(out)-base)
		for i := range n {
			chunk[i] = gen(base + i)
		}
		if b != nil {
			b.StartTimer()
		}

		for i := range n {
			d, err := conv(chunk[i])
			if err != nil {
				return err
			}
			out[base+i] = d
		}
	}
	return nil
}

// benchChunkLen is how many generated sources benchStream holds at once: as
// many as fit in benchChunkBytes, measured rather than assumed because a large
// NetworkPolicy outweighs a minimal Namespace by orders of magnitude.
func benchChunkLen[S any](gen func(i int) S) int {
	const calibN = 256

	probe := make([]S, calibN)
	before := liveHeap()
	for i := range probe {
		probe[i] = gen(i)
	}
	after := liveHeap()
	runtime.KeepAlive(probe)

	perObj := max(float64(after-before)/calibN, 1)
	return max(int(benchChunkBytes/perObj), calibN)
}

// liveBytesPerObject converts a small batch and returns the heap each
// converted object keeps alive, with the sources it was converted from
// dropped. This is what decides whether a case is affordable: the converted
// form is the thing that has to stay resident, and at 1M objects a shape that
// costs a few KiB each does not fit anywhere.
func liveBytesPerObject[S, D any](gen func(i int) S, conv func(S) (D, error)) (float64, error) {
	const calibN = 4096

	out := make([]D, calibN)
	before := liveHeap()
	if err := benchStream(nil, gen, conv, out, calibN); err != nil {
		return 0, err
	}
	// liveHeap collects first, so the chunk benchStream generated is gone by
	// the time this reads: the delta is the converted objects and whatever of
	// their sources they still hold on to, which is the number wanted.
	after := liveHeap()
	runtime.KeepAlive(out)

	return max(float64(after-before)/calibN, 0), nil
}

// liveHeap is the heap still reachable after a collection, so that two
// readings around an allocation differ by what that allocation retained rather
// than by what it churned through.
func liveHeap() int64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapAlloc)
}

// benchLabelKeys are the keys a generated object's labels are drawn from, in
// the order a real object accumulates them: the workload's own identity first
// (what selectors are actually written against), then the controller- and
// tool-injected bookkeeping that inflates the label set of a large object
// without anything ever selecting on it.
var benchLabelKeys = []string{
	"app.kubernetes.io/name",
	"app.kubernetes.io/instance",
	"app.kubernetes.io/component",
	"tier",
	"env",
	"team",
	"region",
	"app.kubernetes.io/part-of",
	"app.kubernetes.io/version",
	"app.kubernetes.io/managed-by",
	"pod-template-hash",
	"security.istio.io/tlsMode",
	"service.istio.io/canonical-name",
	"service.istio.io/canonical-revision",
	"argocd.argoproj.io/instance",
	"helm.sh/chart",
}

func benchLabelKey(j int) string {
	if j < len(benchLabelKeys) {
		return benchLabelKeys[j]
	}
	return fmt.Sprintf("example.com/label-%02d", j-len(benchLabelKeys))
}

// benchLabelValue keeps values distinct per object rather than shared, so that
// conversion sees the same string-header churn a freshly decoded object set
// would, but bounded in cardinality the way a real cluster's are.
func benchLabelValue(i, j int) string {
	return fmt.Sprintf("value-%03d", (i+j*7)%128)
}

func benchLabels(n, i int) map[string]string {
	m := make(map[string]string, n)
	for j := range n {
		m[benchLabelKey(j)] = benchLabelValue(i, j)
	}
	return m
}

// benchNamespaces and benchNodes spread generated pods and policies over a
// plausible number of namespaces and nodes, so the strings repr copies are not
// all the same one.
const (
	benchNamespaces = 1_000
	benchNodes      = 5_000
)

func benchNamespaceName(i int) string {
	return fmt.Sprintf("team-%03d-ns-%05d", i%64, i)
}

func benchNamespace(sc benchScale, i int) *corev1.Namespace {
	name := benchNamespaceName(i)
	// Every namespace carries kubernetes.io/metadata.name, so it takes one of
	// the scale's label slots rather than adding to them.
	labels := benchLabels(sc.labels-1, i)
	labels["kubernetes.io/metadata.name"] = name
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func benchPod(sc benchScale, i int) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: benchNamespaceName(i % benchNamespaces),
			Name:      fmt.Sprintf("workload-%03d-%06x-%05d", i%128, i*2654435761&0xffffff, i),
			Labels:    benchLabels(sc.labels, i),
		},
		Spec: corev1.PodSpec{NodeName: fmt.Sprintf("node-%05d", i%benchNodes)},
	}
	ip := fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff)
	pod.Status.PodIP = ip
	if sc.dualStack {
		// PodIPs is authoritative when set, so this is also the case that
		// exercises the slice path in podIPs rather than the single-IP one.
		// Two groups, so the address stays well-formed past i = 65535.
		v6 := fmt.Sprintf("fd00::%x:%x", i>>16&0xffff, i&0xffff)
		pod.Status.PodIPs = []corev1.PodIP{{IP: ip}, {IP: v6}}
	}
	return pod
}

func benchPolicy(sc benchScale, i int) *networkingv1.NetworkPolicy {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: benchNamespaceName(i % benchNamespaces),
			Name:      fmt.Sprintf("allow-%05d", i),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: benchSelector(sc.matchLabels, sc.matchExprs, i),
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	np.Spec.Ingress = make([]networkingv1.NetworkPolicyIngressRule, sc.rules)
	for r := range np.Spec.Ingress {
		np.Spec.Ingress[r] = networkingv1.NetworkPolicyIngressRule{
			From:  benchPeers(sc, i+r),
			Ports: benchPorts(sc, i+r),
		}
	}
	if sc.egress {
		np.Spec.PolicyTypes = append(np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		np.Spec.Egress = make([]networkingv1.NetworkPolicyEgressRule, sc.rules)
		for r := range np.Spec.Egress {
			np.Spec.Egress[r] = networkingv1.NetworkPolicyEgressRule{
				To:    benchPeers(sc, i+r+1),
				Ports: benchPorts(sc, i+r+1),
			}
		}
	}
	return np
}

func benchSelector(matchLabels, matchExprs, i int) metav1.LabelSelector {
	sel := metav1.LabelSelector{}
	if matchLabels > 0 {
		sel.MatchLabels = benchLabels(matchLabels, i)
	}
	// One of each operator, cycled, since NewPredicate compiles them down
	// different paths and only In/NotIn carry a value list to clone.
	for j := range matchExprs {
		req := metav1.LabelSelectorRequirement{Key: benchLabelKey(matchLabels + j)}
		switch j % 4 {
		case 0:
			req.Operator = metav1.LabelSelectorOpIn
			req.Values = []string{benchLabelValue(i, j), benchLabelValue(i+1, j), benchLabelValue(i+2, j)}
		case 1:
			req.Operator = metav1.LabelSelectorOpNotIn
			req.Values = []string{benchLabelValue(i, j), benchLabelValue(i+1, j)}
		case 2:
			req.Operator = metav1.LabelSelectorOpExists
		case 3:
			req.Operator = metav1.LabelSelectorOpDoesNotExist
		}
		sel.MatchExpressions = append(sel.MatchExpressions, req)
	}
	return sel
}

func benchPeers(sc benchScale, i int) []networkingv1.NetworkPolicyPeer {
	peers := make([]networkingv1.NetworkPolicyPeer, sc.peers)
	for j := range peers {
		// Every fourth peer is an ipBlock, which converts by parsing CIDRs
		// rather than by compiling selectors.
		if sc.peers > 3 && j%4 == 3 {
			peers[j].IPBlock = &networkingv1.IPBlock{
				CIDR:   fmt.Sprintf("10.%d.0.0/16", (i+j)%256),
				Except: []string{fmt.Sprintf("10.%d.%d.0/24", (i+j)%256, j%256)},
			}
			continue
		}
		podSel := benchSelector(sc.peerMatchLabels, sc.peerMatchExprs, i+j)
		peers[j].PodSelector = &podSel
		// An omitted namespaceSelector — the "same namespace as the policy"
		// peer — is the common case, so only half the peers get an explicit
		// one; both are converted differently (nil yields a nil Predicate).
		if j%2 == 1 {
			nsSel := benchSelector(sc.peerMatchLabels, sc.peerMatchExprs, i+j+1)
			peers[j].NamespaceSelector = &nsSel
		}
	}
	return peers
}

var benchProtocols = []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP}

func benchPorts(sc benchScale, i int) []networkingv1.NetworkPolicyPort {
	ports := make([]networkingv1.NetworkPolicyPort, sc.ports)
	for j := range ports {
		proto := benchProtocols[(i+j)%len(benchProtocols)]
		port := intstr.FromInt32(int32(8000 + (i+j)%1000))
		ports[j].Protocol = &proto
		ports[j].Port = &port
		// Ranges are the more expensive form (a second bounds-checked copy),
		// so a third of the ports are one.
		if j%3 == 2 {
			end := port.IntVal + 16
			ports[j].EndPort = &end
		}
	}
	return ports
}
