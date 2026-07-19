package indexed

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/bowei/xnp/internal/policy/basic"
	"github.com/bowei/xnp/internal/policy/common"
	"github.com/bowei/xnp/internal/policy/common/testutil"
	"github.com/bowei/xnp/internal/store"
)

// renderBasic runs basic.RenderBasic into a fresh store, failing the test on
// error so every call site doesn't have to. This package's differential
// tests use it as the correctness reference for RenderIndexed.
func renderBasic(t *testing.T, in *common.Input) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := basic.RenderBasic(in, s); err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}
	return s
}

// renderIndexed runs RenderIndexed into a fresh store, failing the test on
// error so every call site doesn't have to.
func renderIndexed(t *testing.T, in *common.Input) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderIndexed(in, s); err != nil {
		t.Fatalf("RenderIndexed: %v", err)
	}
	return s
}

// TestRenderIndexed_MatchesBasic is a differential test: RenderIndexed must
// produce exactly the same tuple set as RenderBasic for the same input.
// This is the test that would actually catch a bug in the identity/index
// machinery (e.g. a stale SelectorID, a missed namespace bucket).
func TestRenderIndexed_MatchesBasic(t *testing.T) {
	policies, pods, namespaces := testutil.BuildRandomFixture(1)
	in := testutil.MustInput(t, policies, pods, namespaces)

	want := renderBasic(t, in)
	got := renderIndexed(t, in)
	testutil.AssertTuples(t, got.List(), want.List())
}

func TestRenderIndexed_Empty(t *testing.T) {
	in := testutil.MustInput(t, nil, nil, nil)
	store := renderIndexed(t, in)
	if store.Len() != 0 {
		t.Fatalf("expected no tuples, got %+v", store.List())
	}
}

func TestRenderIndexed_NamedPortUnsupported(t *testing.T) {
	pods := []*corev1.Pod{testutil.Pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
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

	if _, err := common.NewInput([]*networkingv1.NetworkPolicy{np}, pods, nil); err == nil {
		t.Fatal("NewInput: expected error for named port, got nil")
	}
}

func TestRenderIndexed_MatchExpressions(t *testing.T) {
	policies, pods, namespaces := testutil.BuildExpressionFixture()
	in := testutil.MustInput(t, policies, pods, namespaces)

	want := renderBasic(t, in)
	got := renderIndexed(t, in)
	testutil.AssertTuples(t, got.List(), want.List())
}

// TestRenderIndexed_ReplicatedLabelSets exercises the actual scenario
// motivating identity classes: many pods sharing few distinct label sets,
// across multiple namespaces, with namespaceSelector peers.
func TestRenderIndexed_ReplicatedLabelSets(t *testing.T) {
	var pods []*corev1.Pod
	var namespaces []*corev1.Namespace
	for n := range 4 {
		nsName := fmt.Sprintf("ns-%d", n)
		namespaces = append(namespaces, testutil.Namespace(nsName, map[string]string{"team": fmt.Sprintf("team-%d", n%2)}))
		for _, app := range []string{"web", "worker"} {
			for r := range 25 {
				pods = append(pods, testutil.Pod(nsName, fmt.Sprintf("%s-%d", app, r), fmt.Sprintf("10.%d.0.%d", n, r), map[string]string{"app": app}))
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
	in := testutil.MustInput(t, policies, pods, namespaces)

	want := renderBasic(t, in)
	got := renderIndexed(t, in)
	testutil.AssertTuples(t, got.List(), want.List())
}
