package init

// Startup benchmark: what the whole §Startup sequence costs on a cluster of
// 10k pods and 10k NetworkPolicies — how long one resync takes, and how much
// memory the three stores are left holding afterwards.
//
//	go test ./internal/init/ -run '^$' -bench BenchmarkDo -benchmem
//
// ns/op is one full resync of the whole cluster, not one object. B/op is
// everything Init churns through, most of which is garbage by the time it
// returns; the figure that matters for capacity is B/live, the heap still
// reachable once Do has returned.
//
// B/live is measured as a delta over a heap that already has the API objects
// live, rather than as a total, because a repr mirror shares what it copied
// out — every string, and a Pod's label map whole. What the stores cost is what
// they add on top of the informer caches they were built from, and those caches
// are a cost client-go imposes whether or not Init runs. The flip side is that
// B/live is not what the process would use if the caches were dropped: the
// shared maps and strings would stay, charged to the stores instead.
//
// The cluster's shape is a knob, not a finding. The counts and the label
// vocabulary below are one plausible cluster, and the pod-side numbers move
// with how well the pods collapse into equivalence classes — hence classes/op,
// reported alongside, since 10k pods in 1k classes and 10k pods in 10k classes
// are different measurements. Everything here is generated: no I/O, no
// decoding, so this is Init's own cost and not a resync's.

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/bowei/xnp/internal/store"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	benchPods       = 10_000
	benchPolicies   = 10_000
	benchNamespaces = 500

	// benchReplicas is how many pods share one workload's labels. Replicas of a
	// Deployment carry identical labels, so this is what decides how far the
	// pods collapse into equivalence classes, and with it the pod side of both
	// the time and the memory: a class is compiled once and a pod links to it.
	benchReplicas = 10
)

// benchLabelKeys is the vocabulary pods and policy selectors are drawn from.
// The split at benchSelectedKeys is the load-bearing part, not the total count:
// only the first group is named by a selector, so only it ends up in
// RelevantKeys, and a pod is grouped by its labels projected onto that. The
// rest are the controller- and tool-injected labels that inflate a real pod's
// label set without anything ever selecting on them — carried into the store
// (repr.Pod keeps the raw map, to reproject it) but never compiled.
var benchLabelKeys = []string{
	"app.kubernetes.io/name",
	"app.kubernetes.io/component",
	"tier",
	"env",
	// benchSelectedKeys ends here.
	"app.kubernetes.io/instance",
	"app.kubernetes.io/version",
	"app.kubernetes.io/managed-by",
	"pod-template-hash",
	"security.istio.io/tlsMode",
	"service.istio.io/canonical-name",
	"argocd.argoproj.io/instance",
	"helm.sh/chart",
}

const benchSelectedKeys = 4

// benchLabelValue keeps values distinct per workload but low in cardinality,
// the way a real cluster's are: kv.KV's signatures reject on hash before
// comparing elements, so a set of values that never collides is not the case
// the matching path actually runs.
func benchLabelValue(workload, j int) string {
	return fmt.Sprintf("value-%03d", (workload+j*7)%128)
}

func benchLabels(workload int) map[string]string {
	m := make(map[string]string, len(benchLabelKeys))
	for j, k := range benchLabelKeys {
		m[k] = benchLabelValue(workload, j)
	}
	return m
}

func benchNamespaceName(i int) string {
	return fmt.Sprintf("team-%03d-ns-%05d", i%64, i)
}

func benchNamespace(i int) *corev1.Namespace {
	name := benchNamespaceName(i)
	labels := benchLabels(i)
	// Every namespace carries this one, and it is the label a namespaceSelector
	// most often names.
	labels["kubernetes.io/metadata.name"] = name
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// benchPod builds pod i as one replica of workload i/benchReplicas, which lives
// in one namespace. Spreading the replicas of a workload across namespaces
// instead would put every pod in a class of its own, since classes are grouped
// by namespace first.
func benchPod(i int) *corev1.Pod {
	workload := i / benchReplicas
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: benchNamespaceName(workload % benchNamespaces),
			Name:      fmt.Sprintf("workload-%05d-%06x-%05d", workload, i*2654435761&0xffffff, i),
			Labels:    benchLabels(workload),
		},
		Spec: corev1.PodSpec{NodeName: fmt.Sprintf("node-%05d", i%5_000)},
	}
	ip := fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff)
	pod.Status.PodIP = ip
	pod.Status.PodIPs = []corev1.PodIP{
		{IP: ip},
		// Two groups, so the address stays well-formed past i = 65535.
		{IP: fmt.Sprintf("fd00::%x:%x", i>>16&0xffff, i&0xffff)},
	}
	return pod
}

// benchPolicy builds a policy of middling size: a selector picking one
// workload, and a short allow-list of peers and ports in both directions.
func benchPolicy(i int) *v1.NetworkPolicy {
	np := &v1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: benchNamespaceName(i % benchNamespaces),
			Name:      fmt.Sprintf("allow-%05d", i),
		},
		Spec: v1.NetworkPolicySpec{
			PodSelector: benchSelector(i),
			PolicyTypes: []v1.PolicyType{v1.PolicyTypeIngress, v1.PolicyTypeEgress},
		},
	}
	const rules = 3
	np.Spec.Ingress = make([]v1.NetworkPolicyIngressRule, rules)
	np.Spec.Egress = make([]v1.NetworkPolicyEgressRule, rules)
	for r := range rules {
		np.Spec.Ingress[r] = v1.NetworkPolicyIngressRule{From: benchPeers(i + r), Ports: benchPorts(i + r)}
		np.Spec.Egress[r] = v1.NetworkPolicyEgressRule{To: benchPeers(i + r + 1), Ports: benchPorts(i + r + 1)}
	}
	return np
}

// benchSelector names only keys below benchSelectedKeys, so that RelevantKeys
// converges on that group however many policies are generated. A generator that
// gave each policy its own key would grow the relevant set with the policy
// count and reproject every pod onto a wider set — a real effect, but a
// different benchmark from this one.
func benchSelector(i int) metav1.LabelSelector {
	return metav1.LabelSelector{
		MatchLabels: map[string]string{
			benchLabelKeys[0]: benchLabelValue(i, 0),
			benchLabelKeys[1]: benchLabelValue(i, 1),
		},
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      benchLabelKeys[2],
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{benchLabelValue(i, 2), benchLabelValue(i+1, 2)},
		}, {
			Key:      benchLabelKeys[3],
			Operator: metav1.LabelSelectorOpExists,
		}},
	}
}

func benchPeers(i int) []v1.NetworkPolicyPeer {
	const peers = 3
	out := make([]v1.NetworkPolicyPeer, peers)
	for j := range out {
		// One peer in three is an ipBlock, which converts by parsing CIDRs
		// rather than by compiling selectors.
		if j == 2 {
			out[j].IPBlock = &v1.IPBlock{
				CIDR:   fmt.Sprintf("10.%d.0.0/16", (i+j)%256),
				Except: []string{fmt.Sprintf("10.%d.%d.0/24", (i+j)%256, j)},
			}
			continue
		}
		sel := benchSelector(i + j)
		out[j].PodSelector = &sel
		// An omitted namespaceSelector — the "same namespace as the policy"
		// peer — is the common case, so only some peers get an explicit one.
		// The two convert differently: nil yields a nil Predicate.
		if j == 1 {
			nsSel := benchSelector(i + j + 1)
			out[j].NamespaceSelector = &nsSel
		}
	}
	return out
}

var benchProtocols = []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP}

func benchPorts(i int) []v1.NetworkPolicyPort {
	const ports = 3
	out := make([]v1.NetworkPolicyPort, ports)
	for j := range out {
		proto := benchProtocols[(i+j)%len(benchProtocols)]
		port := intstr.FromInt32(int32(8000 + (i+j)%1000))
		out[j].Protocol = &proto
		out[j].Port = &port
		// A range is the more expensive form, so one port in three is one.
		if j == 2 {
			end := port.IntVal + 16
			out[j].EndPort = &end
		}
	}
	return out
}

// benchCluster generates the whole cluster up front and returns the Caches over
// it. The objects stay live for the length of the benchmark, which is the point
// as much as a convenience: an informer cache is resident too, so measuring the
// stores against a heap that holds it is measuring what they actually add.
func benchCluster() Caches {
	policies := make([]*v1.NetworkPolicy, benchPolicies)
	for i := range policies {
		policies[i] = benchPolicy(i)
	}
	pods := make([]*corev1.Pod, benchPods)
	for i := range pods {
		pods[i] = benchPod(i)
	}
	namespaces := make([]*corev1.Namespace, benchNamespaces)
	for i := range namespaces {
		namespaces[i] = benchNamespace(i)
	}
	return Caches{
		NetworkPolicies: policySeq(policies...),
		Pods:            podSeq(pods...),
		Namespaces:      nsSeq(namespaces...),
	}
}

func BenchmarkDo(b *testing.B) {
	caches := benchCluster()

	// The baseline is read with the generated objects live and the stores still
	// empty, so the delta at the end is what Do retained on top of them. Do runs
	// once before it for the same reason a benchmark warms up: the first call
	// grows the heap arenas the rest reuse, and charging that to the delta would
	// read as memory the stores hold.
	if err := Do(newStores(), caches); err != nil {
		b.Fatalf("Do: %v", err)
	}
	baseline := liveHeap()

	var stores store.Stores
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		// Fresh stores each iteration rather than re-initializing one set: Init
		// replaces what a store holds, so reusing them would measure the
		// collector reclaiming the previous iteration's cluster at the same time
		// as it builds this one.
		stores = newStores()
		if err := Do(stores, caches); err != nil {
			b.Fatalf("Do: %v", err)
		}
	}
	b.StopTimer()

	// Exactly one cluster's worth of stores is reachable here — the earlier
	// iterations' are garbage, and liveHeap collects before reading.
	resident := liveHeap() - baseline
	runtime.KeepAlive(stores)
	runtime.KeepAlive(caches)

	var classes int
	for range stores.Pods.Classes().All() {
		classes++
	}

	b.ReportMetric(float64(resident), "B/live")
	b.ReportMetric(float64(classes), "classes/op")
	b.ReportMetric(float64(stores.NetworkPolicies.RelevantKeys().Len()), "relevantKeys/op")
}

// TestBenchCluster checks that the generated cluster is the one the benchmark
// is meant to measure and that every object converts. The benchmark would fail
// on an unconvertible object itself, but only when run: this belongs in
// `go test`, not in a -bench run someone does days later.
func TestBenchCluster(t *testing.T) {
	stores := newStores()
	if err := Do(stores, benchCluster()); err != nil {
		t.Fatalf("Do(benchCluster()): %v", err)
	}

	if got, want := stores.Pods.Len(), benchPods; got != want {
		t.Errorf("Pods.Len() = %d, want %d", got, want)
	}
	if got, want := stores.NetworkPolicies.Len(), benchPolicies; got != want {
		t.Errorf("NetworkPolicies.Len() = %d, want %d", got, want)
	}
	if got, want := stores.Namespaces.Len(), benchNamespaces; got != want {
		t.Errorf("Namespaces.Len() = %d, want %d", got, want)
	}
	// The generated names have to be unique, or the stores silently hold fewer
	// objects than were generated and every figure the benchmark reports is off.
	if got, want := stores.NetworkPolicies.RelevantKeys().Len(), benchSelectedKeys; got != want {
		t.Errorf("RelevantKeys().Len() = %d, want %d", got, want)
	}
}

// liveHeap is the heap still reachable after a collection, so that two readings
// around Do differ by what Do retained rather than by what it churned through.
func liveHeap() int64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapAlloc)
}
