// Package bench measures the startup path: NPolicies.Init, then Pods.Init, then
// Join.Init, the three calls a controller makes once a watch has synced.
//
// The question is how long it takes to process N policies by M pods, and how
// that scales with the three things that make an object expensive: how many
// terms a policy's selectors carry (which sets how much pred.Build has to
// compile and how many keys land in the relevant set), how many labels a pod
// carries (which sets what convert.RelevantLabels walks per pod), and how many
// namespaces the two are spread over (which the key merge walks per
// cross-namespace peer, and which partitions both stores).
//
// The join reads those same axes but not in the same direction, which is why it
// is measured separately as well as in place. Its operands are equivalence
// classes rather than objects, so the pod count reaches it only through how many
// classes the pods fall into, and the predicate count is doing two opposing
// things at once: more terms means more relevant keys and so more classes to
// walk, and at the same time a narrower selector matching fewer of them.
//
// The fixtures live here rather than in the two stores' own test files because
// they are one synthetic cluster shared by both: the relevant labels
// NPolicies.Init computes are what Pods.Init groups against, so pods and
// policies have to select on the same key space to measure anything real. A
// benchmark whose policies name keys no pod carries projects every pod onto the
// empty set and measures the empty case.
package bench

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/convert"
	"github.com/bowei/xnp/internal/npolicies"
	"github.com/bowei/xnp/internal/pods"
	"github.com/bowei/xnp/internal/policyjoin"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The axes the benchmarks sweep. Policies and pods are cluster totals, spread
// round-robin over the namespaces; predicates is the number of terms in each of
// a policy's selectors; labels is the number of labels on each pod.
//
// Namespaces is an axis of its own rather than a fixed backdrop because the
// policy side is not linear in it: a peer carrying a namespaceSelector is
// resolved against every namespace when the relevant labels are merged, making
// that pass O(policies x peers x namespaces), and each namespace the selector
// matches takes a set merge on top. Sweeping it is how a cluster with many
// small namespaces is told apart from one with a few large ones at the same
// object count.
var (
	nsCounts     = []int{10, 100, 1000}
	policyCounts = []int{10, 100, 1000}
	podCounts    = []int{100, 1000, 10000}
	predCounts   = []int{1, 5, 10}
	labelCounts  = []int{5, 10, 20}
)

const (
	// podsPerNode sets how many nodes the pods index is spread over, at
	// roughly what a real cluster packs onto a node.
	podsPerNode = 30

	// valuesPerKey is how many distinct values a label key takes across the
	// pods. It is what decides how many equivalence classes form: with every
	// pod carrying a unique value the classes are one pod each and the eqc
	// index degenerates into a second copy of the pod map, which is not the
	// case the store is built for.
	valuesPerKey = 4

	// numTiers is the cardinality of the namespace label a peer's
	// namespaceSelector selects on, so each such peer reaches about a quarter
	// of the namespaces.
	numTiers = 4

	// joinLabels is the pod label count BenchmarkJoinInit holds fixed, at the
	// top of labelCounts. The join sees the label count only through the
	// classes, and a class is a projection onto the relevant keys -- which are
	// the first predicates keys of the shared space. At or above every
	// predicate count every pod carries every relevant key, the projection is
	// exactly that key set, and the extra labels are dropped without changing a
	// class. Sweeping the axis there would run the same join three times.
	joinLabels = 20
)

func BenchmarkInit(b *testing.B) {
	// The full cross product: this is the headline measurement, and none of the
	// axes shifts the others by a constant -- predicates decide how many keys
	// are relevant, which decides how much of each pod's label set survives the
	// projection, and namespaces decide both how much the key merge walks and
	// how finely the pods are divided. Five axes is a long run; -bench takes a
	// regexp over the sub-benchmark names, which is how a single slice through
	// it is measured.
	//
	// The join runs inside the loop with the two stores because it is the rest
	// of the same startup: it is what turns them into something a renderer can
	// read, and a controller that stopped after Pods.Init has resolved nothing.
	// It also cannot be timed on its own here without keeping the stores each
	// iteration built, which is what the isolated BenchmarkJoinInit is for.
	for _, ns := range nsCounts {
		for _, np := range policyCounts {
			for _, m := range podCounts {
				for _, pred := range predCounts {
					for _, labels := range labelCounts {
						name := fmt.Sprintf("ns=%d/policies=%d/pods=%d/pred=%d/labels=%d", ns, np, m, pred, labels)
						b.Run(name, func(b *testing.B) {
							namespaces := makeNamespaces(ns)
							nsLabels := compileNamespaces(namespaces)
							policies := policyFixture(policySpec{namespaces: ns, policies: np, predicates: pred})
							podList := podFixture(podSpec{namespaces: ns, pods: m, labels: labels})

							b.ReportAllocs()
							for b.Loop() {
								nps := npolicies.New()
								if err := nps.Init(namespaces, npSeq(policies)); err != nil {
									b.Fatal(err)
								}
								p := pods.New()
								if err := p.Init(nps.RLabels(), podSeq(podList)); err != nil {
									b.Fatal(err)
								}
								var j policyjoin.Join
								if err := j.Init(nps, p, nsLabels); err != nil {
									b.Fatal(err)
								}
							}
							reportPerObject(b, np+m)
						})
					}
				}
			}
		}
	}
}

// BenchmarkNPoliciesInit isolates the policy side. The combined benchmark
// cannot separate the two calls -- at 10000 pods and 10 policies the policy
// side is lost in the noise -- and this is where a regression in conversion or
// in the per-namespace key merge shows up.
func BenchmarkNPoliciesInit(b *testing.B) {
	for _, ns := range nsCounts {
		for _, np := range policyCounts {
			for _, pred := range predCounts {
				b.Run(fmt.Sprintf("ns=%d/policies=%d/pred=%d", ns, np, pred), func(b *testing.B) {
					namespaces := makeNamespaces(ns)
					policies := policyFixture(policySpec{namespaces: ns, policies: np, predicates: pred})

					b.ReportAllocs()
					for b.Loop() {
						nps := npolicies.New()
						if err := nps.Init(namespaces, npSeq(policies)); err != nil {
							b.Fatal(err)
						}
					}
					reportPerObject(b, np)
				})
			}
		}
	}
}

// BenchmarkPodsInit isolates the pod side, against the relevant labels of a
// policy set scaled to one policy per five namespaces. The label axis only
// means anything relative to that set: a pod's cost is the projection onto the
// relevant keys, so what is being varied is how many of a pod's labels a policy
// somewhere names.
func BenchmarkPodsInit(b *testing.B) {
	for _, ns := range nsCounts {
		for _, m := range podCounts {
			for _, labels := range labelCounts {
				b.Run(fmt.Sprintf("ns=%d/pods=%d/labels=%d", ns, m, labels), func(b *testing.B) {
					// The policy count tracks the namespace count rather than
					// staying fixed: what this call costs per pod is the size
					// of the relevant set in the pod's own namespace, and a
					// fixed policy count spread over 1000 namespaces leaves
					// most of them with no keys at all and every pod in them in
					// one class.
					nps := npolicies.New()
					policies := policyFixture(policySpec{namespaces: ns, policies: ns * 5, predicates: 5})
					if err := nps.Init(makeNamespaces(ns), npSeq(policies)); err != nil {
						b.Fatal(err)
					}
					rl := nps.RLabels()
					podList := podFixture(podSpec{namespaces: ns, pods: m, labels: labels})

					b.ReportAllocs()
					for b.Loop() {
						p := pods.New()
						if err := p.Init(rl, podSeq(podList)); err != nil {
							b.Fatal(err)
						}
					}
					reportPerObject(b, m)
				})
			}
		}
	}
}

// BenchmarkJoinInit isolates the join, against stores the two earlier calls
// built. Building them here rather than hand-rolling classes is not a
// convenience: the relevant labels the policies contribute are what the classes
// are projected onto, so classes built any other way are a projection the
// controller never produces and the selectors would be matched against the
// wrong key set.
//
// The label axis is fixed (see joinLabels) and the other four are swept,
// because none of them reaches the join the way it reaches the stores. The pod
// count arrives only as how many classes the pods fall into, which saturates:
// once every projection the key set admits has a pod in it, ten times the pods
// is the same join. The namespace count is the one that can grow the output
// superlinearly, since a peer's namespaceSelector matches about a quarter of
// the namespaces and pulls each one's classes into every rule carrying it.
func BenchmarkJoinInit(b *testing.B) {
	for _, ns := range nsCounts {
		for _, np := range policyCounts {
			for _, m := range podCounts {
				for _, pred := range predCounts {
					name := fmt.Sprintf("ns=%d/policies=%d/pods=%d/pred=%d", ns, np, m, pred)
					b.Run(name, func(b *testing.B) {
						namespaces := makeNamespaces(ns)
						nsLabels := compileNamespaces(namespaces)

						nps := npolicies.New()
						policies := policyFixture(policySpec{namespaces: ns, policies: np, predicates: pred})
						if err := nps.Init(namespaces, npSeq(policies)); err != nil {
							b.Fatal(err)
						}
						p := pods.New()
						podList := podFixture(podSpec{namespaces: ns, pods: m, labels: joinLabels})
						if err := p.Init(nps.RLabels(), podSeq(podList)); err != nil {
							b.Fatal(err)
						}

						b.ReportAllocs()
						for b.Loop() {
							var j policyjoin.Join
							if err := j.Init(nps, p, nsLabels); err != nil {
								b.Fatal(err)
							}
						}
						// Per policy, not per object: the join walks the
						// policies and never the pods. The figure is not
						// expected flat across the pod and namespace axes --
						// what one policy costs is how many classes its rules
						// reach -- and that is the point of reporting it.
						reportPerObject(b, np)
					})
				}
			}
		}
	}
}

// compileNamespaces builds the namespace label sets the join resolves
// namespaceSelectors against. The controller compiles them once from the synced
// cache and hands the same map to NPolicies.Init and Join.Init, so they are
// fixture here rather than loop body: NPolicies.Init compiles its own copy
// internally and is already paying for that inside the measurement, and timing
// a second pass over the namespaces would charge the join for it twice.
func compileNamespaces(namespaces map[string]*corev1.Namespace) map[string]*kv.KV {
	out := make(map[string]*kv.KV, len(namespaces))
	for name, ns := range namespaces {
		labels := convert.LabelsToKV(ns.Labels)
		out[name] = &labels
	}
	return out
}

// reportPerObject adds the per-object cost alongside ns/op. The sweep spans two
// orders of magnitude on both size axes, where ns/op says only that a bigger
// cluster takes longer; ns/obj is what says whether it is doing so linearly.
func reportPerObject(b *testing.B, objects int) {
	b.Helper()
	if b.N == 0 || objects == 0 {
		return
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(objects), "ns/obj")
}

// Fixtures.
//
// Generation is deterministic -- every value is arithmetic on the object's
// index -- so a run is comparable against the one before it without a seed
// having to be carried anywhere.
//
// Each sub-benchmark builds its own cluster and drops it on the way out rather
// than sharing one out of a cache. Nothing is saved by caching: b.Loop resets
// the timer on its first call, so everything built ahead of the loop is already
// outside the measurement. What caching would cost is real -- the sweep asks
// for nine distinct shapes at 10000 pods, and holding them all live puts a few
// hundred MB of fixture in front of the collector while the thing being
// measured is itself allocation-heavy.
type podSpec struct {
	// namespaces is part of the spec because it is what the pods are spread
	// over: the same pod count in 10 namespaces and in 1000 is not the same
	// cluster.
	namespaces int
	pods       int
	labels     int
}

type policySpec struct {
	namespaces int
	policies   int
	predicates int
}

func podFixture(spec podSpec) []*corev1.Pod {
	out := make([]*corev1.Pod, spec.pods)
	for i := range out {
		out[i] = makePod(i, spec.labels, spec.namespaces)
	}
	return out
}

func policyFixture(spec policySpec) []*networkingv1.NetworkPolicy {
	out := make([]*networkingv1.NetworkPolicy, spec.policies)
	for i := range out {
		out[i] = makePolicy(i, spec.predicates, spec.namespaces)
	}
	return out
}

func makeNamespaces(n int) map[string]*corev1.Namespace {
	out := make(map[string]*corev1.Namespace, n)
	for i := range n {
		name := nsName(i, n)
		out[name] = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"tier": tier(i),
					"team": "team" + strconv.Itoa(i%7),
				},
			},
		}
	}
	return out
}

// makePod builds pod idx carrying labels labels, on the keys the policies
// select on, in one of namespaces namespaces. The value of key i varies with
// two bits of idx, so the low keys turn over quickly and the high ones barely
// at all: the pods fall into classes of a few hundred at the sizes here, with
// the class count growing with the label count rather than with the pod count,
// which is the shape the eqc index is meant to exploit.
func makePod(idx, labels, namespaces int) *corev1.Pod {
	l := make(map[string]string, labels)
	for i := range labels {
		l[labelKey(i)] = "v" + strconv.Itoa((idx>>(2*min(i, 14)))%valuesPerKey)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: nsName(idx, namespaces),
			Name:      "pod" + strconv.Itoa(idx),
			Labels:    l,
		},
		Spec: corev1.PodSpec{
			NodeName: "node" + strconv.Itoa(idx/podsPerNode),
		},
		Status: corev1.PodStatus{
			// Set so that conversion pays for the netip parse it pays for in a
			// real cluster. PodIPs is the dual-stack field and the only one
			// read when it is present, so the single-stack PodIP is left off.
			PodIPs: []corev1.PodIP{{IP: podIP(idx)}},
		},
	}
}

// makePolicy builds policy idx with n terms in each of its selectors, in one of
// namespaces namespaces.
//
// Every policy carries an ingress and an egress rule, and each rule carries
// three peers: one selecting pods in the policy's own namespace, one reaching
// across namespaces through a namespaceSelector, and one ipBlock. The
// cross-namespace peer is the expensive one -- it is what makes the key merge
// walk the namespaces -- and a real policy set has plenty of them, so leaving
// it out would measure a cluster nobody runs.
func makePolicy(idx, n, namespaces int) *networkingv1.NetworkPolicy {
	port := intstr.FromInt32(8080)
	tcp := corev1.ProtocolTCP
	ports := []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}

	peers := func(seed int) []networkingv1.NetworkPolicyPeer {
		return []networkingv1.NetworkPolicyPeer{
			{PodSelector: selector(n, seed)},
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"tier": tier(seed)},
				},
				PodSelector: selector(n, seed+1),
			},
			{IPBlock: &networkingv1.IPBlock{
				CIDR:   "10.0.0.0/8",
				Except: []string{"10.1.0.0/16"},
			}},
		}
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: nsName(idx, namespaces),
			Name:      "np" + strconv.Itoa(idx),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: *selector(n, idx),
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  peers(idx),
				Ports: ports,
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    peers(idx + 2),
				Ports: ports,
			}},
		},
	}
}

// selector builds a selector of n terms on the first n keys of the shared key
// space, alternating matchLabels with a two-value In expression. The mix is
// deliberate: the two compile down different paths in pred.Build, and only
// matchExpressions can produce the operators the key merge has to account for
// separately from the values.
func selector(n, seed int) *metav1.LabelSelector {
	sel := &metav1.LabelSelector{}
	for i := range n {
		v := (seed + i) % valuesPerKey
		if i%2 == 0 {
			if sel.MatchLabels == nil {
				sel.MatchLabels = map[string]string{}
			}
			sel.MatchLabels[labelKey(i)] = "v" + strconv.Itoa(v)
			continue
		}
		sel.MatchExpressions = append(sel.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      labelKey(i),
			Operator: metav1.LabelSelectorOpIn,
			Values: []string{
				"v" + strconv.Itoa(v),
				"v" + strconv.Itoa((v+1)%valuesPerKey),
			},
		})
	}
	return sel
}

func labelKey(i int) string { return "k" + strconv.Itoa(i) }

// nsName spreads object i round-robin over n namespaces. A cluster with more
// namespaces than objects of a kind leaves the tail of them empty of that kind,
// which is what a cluster of many small namespaces looks like: plenty of them
// hold pods and no policy, or the other way round.
func nsName(i, n int) string { return "ns" + strconv.Itoa(i%n) }

func tier(i int) string { return "t" + strconv.Itoa(i%numTiers) }

func podIP(idx int) string {
	return fmt.Sprintf("10.%d.%d.%d", (idx>>16)&0xff, (idx>>8)&0xff, idx&0xff)
}

func podSeq(list []*corev1.Pod) func(yield func(*corev1.Pod) bool) {
	return func(yield func(*corev1.Pod) bool) {
		for _, p := range list {
			if !yield(p) {
				return
			}
		}
	}
}

func npSeq(list []*networkingv1.NetworkPolicy) func(yield func(*networkingv1.NetworkPolicy) bool) {
	return func(yield func(*networkingv1.NetworkPolicy) bool) {
		for _, np := range list {
			if !yield(np) {
				return
			}
		}
	}
}
