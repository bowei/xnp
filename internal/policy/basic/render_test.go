package basic

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/bowei/xnp/internal/policy/common"
	"github.com/bowei/xnp/internal/policy/common/testutil"
	"github.com/bowei/xnp/internal/store"
)

// renderBasic runs RenderBasic into a fresh store, failing the test on error
// so every call site doesn't have to.
func renderBasic(t *testing.T, in *common.Input) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderBasic(in, s); err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}
	return s
}

// renderParallel is renderBasic's RenderParallel counterpart.
func renderParallel(t *testing.T, in *common.Input, workers int) store.Store {
	t.Helper()
	s := store.NewStore(0)
	if err := RenderParallel(in, s, workers); err != nil {
		t.Fatalf("RenderParallel: %v", err)
	}
	return s
}

// TestRenderParallel_MatchesBasic is a differential test: RenderParallel
// must produce exactly the same tuple set as RenderBasic for the same
// input, across a range of worker counts. This is the test that would
// actually catch a concurrency bug (e.g. a goroutine writing to the wrong
// result slot); run with -race to also catch data races.
func TestRenderParallel_MatchesBasic(t *testing.T) {
	policies, pods, namespaces := testutil.BuildRandomFixture(1)
	in := testutil.MustInput(t, policies, pods, namespaces)

	want := renderBasic(t, in)

	for _, workers := range []int{0, 1, 2, 3, 8, 64} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			got := renderParallel(t, in, workers)
			testutil.AssertTuples(t, got.List(), want.List())
		})
	}
}

func TestRenderParallel_Empty(t *testing.T) {
	in := testutil.MustInput(t, nil, nil, nil)
	store := renderParallel(t, in, 4)
	if store.Len() != 0 {
		t.Fatalf("expected no tuples, got %+v", store.List())
	}
}

func TestRenderParallel_NamedPortUnsupported(t *testing.T) {
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
