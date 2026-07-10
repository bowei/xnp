package policy

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TestRenderIndexed_MatchesBasic is a differential test: RenderIndexed must
// produce exactly the same tuple set as RenderBasic for the same input.
// This is the test that would actually catch a bug in the identity/index
// machinery (e.g. a stale SelectorID, a missed namespace bucket).
func TestRenderIndexed_MatchesBasic(t *testing.T) {
	policies, pods, namespaces := buildRandomFixture(1)
	in := mustInput(t, policies, pods, namespaces)

	want, err := RenderBasic(in)
	if err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}
	got, err := RenderIndexed(in)
	if err != nil {
		t.Fatalf("RenderIndexed: %v", err)
	}
	assertTuples(t, got.List(), want.List())
}

func TestRenderIndexed_Empty(t *testing.T) {
	in := mustInput(t, nil, nil, nil)
	store, err := RenderIndexed(in)
	if err != nil {
		t.Fatalf("RenderIndexed: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("expected no tuples, got %+v", store.List())
	}
}

func TestRenderIndexed_NamedPortUnsupported(t *testing.T) {
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

	if _, err := NewInput([]*networkingv1.NetworkPolicy{np}, pods, nil); err == nil {
		t.Fatal("NewInput: expected error for named port, got nil")
	}
}

// buildExpressionFixture exercises every metav1.LabelSelector operator
// (Exists, DoesNotExist, In with one and multiple values, NotIn), which
// buildRandomFixture doesn't touch since it only generates MatchLabels.
// These are exactly the requirement kinds compileSelector splits into
// exact/existsKeys/residual, so this is the test that would catch a
// residual-evaluation bug.
func buildExpressionFixture() ([]*networkingv1.NetworkPolicy, []*corev1.Pod, []*corev1.Namespace) {
	pods := []*corev1.Pod{
		pod("ns", "a", "10.0.0.1", map[string]string{"app": "web", "tier": "frontend", "canary": "true"}),
		pod("ns", "b", "10.0.0.2", map[string]string{"app": "web", "tier": "backend"}),
		pod("ns", "c", "10.0.0.3", map[string]string{"app": "worker", "tier": "backend"}),
		pod("ns", "d", "10.0.0.4", map[string]string{"app": "web"}), // no tier
		pod("ns", "e", "10.0.0.5", nil),
	}

	mkPolicy := func(name string, expr []metav1.LabelSelectorRequirement, peerExpr []metav1.LabelSelectorRequirement) *networkingv1.NetworkPolicy {
		return &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchExpressions: expr},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						PodSelector: &metav1.LabelSelector{MatchExpressions: peerExpr},
					}},
				}},
			},
		}
	}

	policies := []*networkingv1.NetworkPolicy{
		mkPolicy("exists",
			[]metav1.LabelSelectorRequirement{{Key: "canary", Operator: metav1.LabelSelectorOpExists}},
			[]metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"web"}}}),
		mkPolicy("does-not-exist",
			[]metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpDoesNotExist}},
			[]metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"worker"}}}),
		mkPolicy("in-multi",
			[]metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"web", "worker"}}},
			[]metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"frontend", "backend"}}}),
		mkPolicy("not-in",
			[]metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"backend"}}},
			nil),
	}
	// The "not-in" policy above passes peerExpr=nil, which produces an
	// empty (match-all) peer selector; give it a concrete one instead.
	policies[3].Spec.Ingress[0].From[0].PodSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "canary", Operator: metav1.LabelSelectorOpExists}},
	}

	return policies, pods, nil
}

func TestRenderIndexed_MatchExpressions(t *testing.T) {
	policies, pods, namespaces := buildExpressionFixture()
	in := mustInput(t, policies, pods, namespaces)

	want, err := RenderBasic(in)
	if err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}
	got, err := RenderIndexed(in)
	if err != nil {
		t.Fatalf("RenderIndexed: %v", err)
	}
	assertTuples(t, got.List(), want.List())
}

// TestRenderIndexed_ReplicatedLabelSets exercises the actual scenario
// motivating identity classes: many pods sharing few distinct label sets,
// across multiple namespaces, with namespaceSelector peers.
func TestRenderIndexed_ReplicatedLabelSets(t *testing.T) {
	var pods []*corev1.Pod
	var namespaces []*corev1.Namespace
	for n := range 4 {
		nsName := fmt.Sprintf("ns-%d", n)
		namespaces = append(namespaces, namespace(nsName, map[string]string{"team": fmt.Sprintf("team-%d", n%2)}))
		for _, app := range []string{"web", "worker"} {
			for r := range 25 {
				pods = append(pods, pod(nsName, fmt.Sprintf("%s-%d", app, r), fmt.Sprintf("10.%d.0.%d", n, r), map[string]string{"app": app}))
			}
		}
	}

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-0", Name: "allow-web-from-team-1"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}},
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "team-1"}},
				}},
			}},
		},
	}
	policies := []*networkingv1.NetworkPolicy{np}
	in := mustInput(t, policies, pods, namespaces)

	want, err := RenderBasic(in)
	if err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}
	got, err := RenderIndexed(in)
	if err != nil {
		t.Fatalf("RenderIndexed: %v", err)
	}
	assertTuples(t, got.List(), want.List())
}
