package indexed

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/bowei/xnp/internal/policy/common"
	"github.com/bowei/xnp/internal/policy/common/testutil"
	"github.com/bowei/xnp/internal/tuple"
)

// This file holds the hand-written NetworkPolicy fixtures originally written
// against RenderBasic alone. Every case doubles as a differential check
// (assertIndexedMatchesBasic) so RenderIndexed is exercised against the same
// edge cases (unisolated pods, IPBlocks, port ranges, PolicyTypes
// defaulting, namespaceSelector peers, ...) without duplicating each test's
// fixture — see TestRenderIndexed_MatchesBasic/MatchExpressions/
// ReplicatedLabelSets in render_indexed_test.go for the randomized/generated
// counterparts. TestRenderIndexed_NamedPortUnsupported already covers the
// named-port-rejected-at-NewInput case, so it isn't repeated here.

// assertIndexedMatchesBasic is a differential check run alongside every
// hand-written fixture in this file.
func assertIndexedMatchesBasic(t *testing.T, in *common.Input, basicTuples []tuple.Tuple) {
	t.Helper()
	indexed := renderIndexed(t, in)
	testutil.AssertTuples(t, indexed.List(), basicTuples)
}

func TestRenderIndexed_UnisolatedPod(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "a", "10.0.0.1", nil)}
	in := testutil.MustInput(t, nil, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "a"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	testutil.AssertTuples(t, tuples, want)
}

func TestRenderIndexed_IsolatedNoRules(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "a", "10.0.0.1", map[string]string{"app": "a"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "deny-all"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: testutil.NSName("ns", "a"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictDeny},
	}
	testutil.AssertTuples(t, tuples, want)
}

func TestRenderIndexed_EmptyFromAndPorts(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "a", "10.0.0.1", map[string]string{"app": "a"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-all-ingress"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		// Egress not in PolicyTypes -> unisolated -> allow-all catch-all.
		{Pod: testutil.NSName("ns", "a"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	testutil.AssertTuples(t, tuples, want)
}

func TestRenderIndexed_PodSelectorPeer(t *testing.T) {
	pods := []*corev1.Pod{
		testutil.Pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"}),
		testutil.Pod("ns", "client-1", "10.0.0.2", map[string]string{"app": "client"}),
		testutil.Pod("ns", "client-2", "10.0.0.3", map[string]string{"app": "client"}),
		testutil.Pod("ns", "other", "10.0.0.4", map[string]string{"app": "other"}),
	}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-client"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "client"}},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: new(corev1.ProtocolTCP),
					Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 80},
				}},
			}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	// Only the "web" pod is selected/isolated; others are unisolated.
	var webTuples []tuple.Tuple
	for _, tup := range tuples {
		if tup.Pod == testutil.NSName("ns", "web") {
			webTuples = append(webTuples, tup)
		}
	}

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.2", Dest: "10.0.0.1", Port: new(int32(80)), Protocol: tuple.ProtocolTCP, Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.3", Dest: "10.0.0.1", Port: new(int32(80)), Protocol: tuple.ProtocolTCP, Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	testutil.AssertTuples(t, webTuples, want)
}

func TestRenderIndexed_NamespaceSelectorPeer(t *testing.T) {
	namespaces := []*corev1.Namespace{
		testutil.Namespace("ns-a", map[string]string{"team": "a"}),
		testutil.Namespace("ns-b", map[string]string{"team": "b"}),
	}
	pods := []*corev1.Pod{
		testutil.Pod("ns-a", "web", "10.0.0.1", map[string]string{"app": "web"}),
		testutil.Pod("ns-b", "peer", "10.0.1.1", map[string]string{"app": "peer"}),
	}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "allow-team-b"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "b"}},
				}},
			}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, namespaces)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	var webTuples []tuple.Tuple
	for _, tup := range tuples {
		if tup.Pod == testutil.NSName("ns-a", "web") {
			webTuples = append(webTuples, tup)
		}
	}

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns-a", "web"), Src: "10.0.1.1", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns-a", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: testutil.NSName("ns-a", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	testutil.AssertTuples(t, webTuples, want)
}

func TestRenderIndexed_IPBlockPeer(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-cidr"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					IPBlock: &networkingv1.IPBlock{CIDR: "192.168.0.0/16"},
				}},
			}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "web"), Src: "192.168.0.0/16", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	testutil.AssertTuples(t, tuples, want)
}

func TestRenderIndexed_PortRange(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-range"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: new(corev1.ProtocolTCP),
					Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 8000},
					EndPort:  new(int32(8080)),
				}},
			}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "web"), Src: "", Dest: "10.0.0.1", Port: new(int32(8000)), EndPort: new(int32(8080)), Protocol: tuple.ProtocolTCP, Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	testutil.AssertTuples(t, tuples, want)
}

func TestRenderIndexed_MultiplePoliciesUnioned(t *testing.T) {
	pods := []*corev1.Pod{
		testutil.Pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"}),
		testutil.Pod("ns", "a", "10.0.0.2", map[string]string{"app": "a"}),
		testutil.Pod("ns", "b", "10.0.0.3", map[string]string{"app": "b"}),
	}
	npA := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-a"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}}}},
			}},
		},
	}
	npB := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-b"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "b"}}}},
			}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{npA, npB}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	var webIngress []tuple.Tuple
	for _, tup := range tuples {
		if tup.Pod == testutil.NSName("ns", "web") && tup.Direction == tuple.DirectionIngress {
			webIngress = append(webIngress, tup)
		}
	}

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.2", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.3", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
	}
	testutil.AssertTuples(t, webIngress, want)
}

func TestRenderIndexed_DefaultPolicyTypes(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}

	// No explicit PolicyTypes, only an Egress rule -> per k8s defaulting,
	// Ingress is still assumed (isolating it with zero rules = deny-all),
	// and Egress is assumed because Egress rules are present.
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "egress-only"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "1.1.1.1/32"}}},
			}},
		},
	}
	in := testutil.MustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: testutil.NSName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.1", Dest: "1.1.1.1/32", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
		{Pod: testutil.NSName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictDeny},
	}
	testutil.AssertTuples(t, tuples, want)
}
