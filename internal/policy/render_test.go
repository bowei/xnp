package policy

import (
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/bowei/xnp/internal/store"
	"github.com/bowei/xnp/internal/tuple"
)

func pod(namespace, name, ip string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// mustInput runs Phase 1 (NewInput) for a test fixture, failing the test on
// conversion error so every Render* call site doesn't have to.
func mustInput(t *testing.T, policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace) *Input {
	t.Helper()
	in, err := NewInput(policies, pods, namespaces)
	if err != nil {
		t.Fatalf("NewInput: %v", err)
	}
	return in
}

// renderBasic runs RenderBasic into a fresh store, failing the test on error
// so every call site doesn't have to.
func renderBasic(t *testing.T, in *Input) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderBasic(in, s); err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}
	return s
}

// renderParallel is renderBasic's RenderParallel counterpart.
func renderParallel(t *testing.T, in *Input, workers int) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderParallel(in, s, workers); err != nil {
		t.Fatalf("RenderParallel: %v", err)
	}
	return s
}

// renderIndexed is renderBasic's RenderIndexed counterpart.
func renderIndexed(t *testing.T, in *Input) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderIndexed(in, s); err != nil {
		t.Fatalf("RenderIndexed: %v", err)
	}
	return s
}

// renderIndexedParallel is renderBasic's RenderIndexedParallel counterpart.
func renderIndexedParallel(t *testing.T, in *Input, workers int) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderIndexedParallel(in, s, workers); err != nil {
		t.Fatalf("RenderIndexedParallel: %v", err)
	}
	return s
}

// sortTuples gives tests a stable order to compare against, independent of
// the (documented but not load-bearing) emission order.
func sortTuples(tuples []tuple.Tuple) []tuple.Tuple {
	out := append([]tuple.Tuple(nil), tuples...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pod != b.Pod {
			return a.Pod.String() < b.Pod.String()
		}
		if a.Direction != b.Direction {
			return a.Direction < b.Direction
		}
		if a.Src != b.Src {
			return a.Src < b.Src
		}
		if a.Dest != b.Dest {
			return a.Dest < b.Dest
		}
		return a.Verdict < b.Verdict
	})
	return out
}

func TestRender_UnisolatedPod(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "a", "10.0.0.1", nil)}
	in := mustInput(t, nil, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: nsName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "a"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	assertTuples(t, tuples, want)
}

func TestRender_IsolatedNoRules(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "a", "10.0.0.1", map[string]string{"app": "a"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "deny-all"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: nsName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: nsName("ns", "a"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictDeny},
	}
	assertTuples(t, tuples, want)
}

func TestRender_EmptyFromAndPorts(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "a", "10.0.0.1", map[string]string{"app": "a"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "allow-all-ingress"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
		},
	}
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: nsName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "a"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		// Egress not in PolicyTypes -> unisolated -> allow-all catch-all.
		{Pod: nsName("ns", "a"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	assertTuples(t, tuples, want)
}

func TestRender_PodSelectorPeer(t *testing.T) {
	pods := []*corev1.Pod{
		pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns", "client-1", "10.0.0.2", map[string]string{"app": "client"}),
		pod("ns", "client-2", "10.0.0.3", map[string]string{"app": "client"}),
		pod("ns", "other", "10.0.0.4", map[string]string{"app": "other"}),
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
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	// Only the "web" pod is selected/isolated; others are unisolated.
	var webTuples []tuple.Tuple
	for _, tup := range tuples {
		if tup.Pod == nsName("ns", "web") {
			webTuples = append(webTuples, tup)
		}
	}

	want := []tuple.Tuple{
		{Pod: nsName("ns", "web"), Src: "10.0.0.2", Dest: "10.0.0.1", Port: new(int32(80)), Protocol: tuple.ProtocolTCP, Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "10.0.0.3", Dest: "10.0.0.1", Port: new(int32(80)), Protocol: tuple.ProtocolTCP, Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: nsName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	assertTuples(t, webTuples, want)
}

func TestRender_NamespaceSelectorPeer(t *testing.T) {
	namespaces := []*corev1.Namespace{
		namespace("ns-a", map[string]string{"team": "a"}),
		namespace("ns-b", map[string]string{"team": "b"}),
	}
	pods := []*corev1.Pod{
		pod("ns-a", "web", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns-b", "peer", "10.0.1.1", map[string]string{"app": "peer"}),
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
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, namespaces)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	var webTuples []tuple.Tuple
	for _, tup := range tuples {
		if tup.Pod == nsName("ns-a", "web") {
			webTuples = append(webTuples, tup)
		}
	}

	want := []tuple.Tuple{
		{Pod: nsName("ns-a", "web"), Src: "10.0.1.1", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns-a", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: nsName("ns-a", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	assertTuples(t, webTuples, want)
}

func TestRender_IPBlockPeer(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
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
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: nsName("ns", "web"), Src: "192.168.0.0/16", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: nsName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	assertTuples(t, tuples, want)
}

func TestRender_PortRange(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
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
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: nsName("ns", "web"), Src: "", Dest: "10.0.0.1", Port: new(int32(8000)), EndPort: new(int32(8080)), Protocol: tuple.ProtocolTCP, Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: nsName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
	}
	assertTuples(t, tuples, want)
}

func TestRender_MultiplePoliciesUnioned(t *testing.T) {
	pods := []*corev1.Pod{
		pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns", "a", "10.0.0.2", map[string]string{"app": "a"}),
		pod("ns", "b", "10.0.0.3", map[string]string{"app": "b"}),
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
	in := mustInput(t, []*networkingv1.NetworkPolicy{npA, npB}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	var webIngress []tuple.Tuple
	for _, tup := range tuples {
		if tup.Pod == nsName("ns", "web") && tup.Direction == tuple.DirectionIngress {
			webIngress = append(webIngress, tup)
		}
	}

	want := []tuple.Tuple{
		{Pod: nsName("ns", "web"), Src: "10.0.0.2", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "10.0.0.3", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
	}
	assertTuples(t, webIngress, want)
}

func TestRender_DefaultPolicyTypes(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}

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
	in := mustInput(t, []*networkingv1.NetworkPolicy{np}, pods, nil)

	store := renderBasic(t, in)
	tuples := store.List()
	assertIndexedMatchesBasic(t, in, tuples)

	want := []tuple.Tuple{
		{Pod: nsName("ns", "web"), Src: "", Dest: "10.0.0.1", Direction: tuple.DirectionIngress, Verdict: tuple.VerdictDeny},
		{Pod: nsName("ns", "web"), Src: "10.0.0.1", Dest: "1.1.1.1/32", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictAllow},
		{Pod: nsName("ns", "web"), Src: "10.0.0.1", Dest: "", Direction: tuple.DirectionEgress, Verdict: tuple.VerdictDeny},
	}
	assertTuples(t, tuples, want)
}

func TestRender_NamedPortUnsupported(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "named-port"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"}}},
			}},
		},
	}

	// Named ports are now rejected at conversion time (NewInput), before any
	// renderer sees the input.
	if _, err := NewInput([]*networkingv1.NetworkPolicy{np}, pods, nil); err == nil {
		t.Fatal("NewInput: expected error for named port, got nil")
	}
}

func nsName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

// assertIndexedMatchesBasic is a differential check run alongside every
// hand-written RenderBasic test case in this file, so RenderIndexed is
// exercised against the same edge cases (unisolated pods, IPBlocks, port
// ranges, PolicyTypes defaulting, namespaceSelector peers, ...) without
// duplicating each test's fixture.
func assertIndexedMatchesBasic(t *testing.T, in *Input, basic []tuple.Tuple) {
	t.Helper()
	indexed := renderIndexed(t, in)
	assertTuples(t, indexed.List(), basic)
}

func assertTuples(t *testing.T, got, want []tuple.Tuple) {
	t.Helper()
	gotSorted, wantSorted := sortTuples(got), sortTuples(want)
	if len(gotSorted) != len(wantSorted) {
		t.Fatalf("tuple count mismatch: got %d, want %d\ngot:  %+v\nwant: %+v", len(gotSorted), len(wantSorted), gotSorted, wantSorted)
	}
	for i := range gotSorted {
		g, w := gotSorted[i], wantSorted[i]
		if g.Pod != w.Pod || g.Src != w.Src || g.Dest != w.Dest || g.Direction != w.Direction || g.Verdict != w.Verdict || g.Protocol != w.Protocol || !int32PtrEqual(g.Port, w.Port) || !int32PtrEqual(g.EndPort, w.EndPort) {
			t.Fatalf("tuple %d mismatch:\ngot:  %+v\nwant: %+v", i, g, w)
		}
	}
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
