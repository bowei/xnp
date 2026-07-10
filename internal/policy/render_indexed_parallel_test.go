package policy

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TestRenderIndexedParallel_MatchesBasic is a differential test:
// RenderIndexedParallel must produce exactly the same tuple set as
// RenderBasic, across a range of worker counts. Run with -race — this is
// the test that would catch a goroutine writing into the wrong templates or
// results slot in either of RenderIndexedParallel's two fan-out phases.
func TestRenderIndexedParallel_MatchesBasic(t *testing.T) {
	policies, pods, namespaces := buildRandomFixture(1)

	want, err := RenderBasic(policies, pods, namespaces)
	if err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}

	for _, workers := range []int{0, 1, 2, 3, 8, 64} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			got, err := RenderIndexedParallel(policies, pods, namespaces, workers)
			if err != nil {
				t.Fatalf("RenderIndexedParallel: %v", err)
			}
			assertTuples(t, got, want)
		})
	}
}

// TestRenderIndexedParallel_ReplicatedLabelSets exercises the case
// RenderIndexedParallel's group-fan-out phase exists for: many pods sharing
// few distinct label sets, spread across multiple namespaces.
func TestRenderIndexedParallel_ReplicatedLabelSets(t *testing.T) {
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

	want, err := RenderBasic(policies, pods, namespaces)
	if err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}

	for _, workers := range []int{0, 1, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			got, err := RenderIndexedParallel(policies, pods, namespaces, workers)
			if err != nil {
				t.Fatalf("RenderIndexedParallel: %v", err)
			}
			assertTuples(t, got, want)
		})
	}
}

func TestRenderIndexedParallel_Empty(t *testing.T) {
	tuples, err := RenderIndexedParallel(nil, nil, nil, 4)
	if err != nil {
		t.Fatalf("RenderIndexedParallel: %v", err)
	}
	if len(tuples) != 0 {
		t.Fatalf("expected no tuples, got %+v", tuples)
	}
}

func TestRenderIndexedParallel_NamedPortUnsupported(t *testing.T) {
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

	if _, err := RenderIndexedParallel([]*networkingv1.NetworkPolicy{np}, pods, nil, 4); err == nil {
		t.Fatal("RenderIndexedParallel: expected error for named port, got nil")
	}
}
