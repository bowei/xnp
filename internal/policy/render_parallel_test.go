package policy

import (
	"fmt"
	"math/rand"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// buildRandomFixture generates a pseudo-random (but deterministic, given
// seed) set of namespaces, pods, and policies large enough to exercise
// RenderParallel's worker split.
func buildRandomFixture(seed int64) ([]*networkingv1.NetworkPolicy, []*corev1.Pod, []*corev1.Namespace) {
	r := rand.New(rand.NewSource(seed))

	const (
		numNamespaces = 5
		podsPerNS     = 8
		numPolicies   = 20
	)

	var namespaces []*corev1.Namespace
	var pods []*corev1.Pod
	teams := []string{"a", "b", "c"}
	apps := []string{"web", "client", "worker", "cache"}

	for n := 0; n < numNamespaces; n++ {
		nsName := fmt.Sprintf("ns-%d", n)
		namespaces = append(namespaces, namespace(nsName, map[string]string{"team": teams[r.Intn(len(teams))]}))
		for p := 0; p < podsPerNS; p++ {
			labels := map[string]string{"app": apps[r.Intn(len(apps))]}
			ip := fmt.Sprintf("10.%d.%d.%d", n, p/256, p%256)
			pods = append(pods, pod(nsName, fmt.Sprintf("pod-%d", p), ip, labels))
		}
	}

	var policies []*networkingv1.NetworkPolicy
	for i := 0; i < numPolicies; i++ {
		nsName := fmt.Sprintf("ns-%d", r.Intn(numNamespaces))
		selApp := apps[r.Intn(len(apps))]
		peerApp := apps[r.Intn(len(apps))]
		policies = append(policies, &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: fmt.Sprintf("policy-%d", i)},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": selApp}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": peerApp}},
					}},
				}},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": teams[r.Intn(len(teams))]}},
					}},
				}},
			},
		})
	}

	return policies, pods, namespaces
}

// TestRenderParallel_MatchesBasic is a differential test: RenderParallel
// must produce exactly the same tuple set as RenderBasic for the same
// input, across a range of worker counts. This is the test that would
// actually catch a concurrency bug (e.g. a goroutine writing to the wrong
// result slot); run with -race to also catch data races.
func TestRenderParallel_MatchesBasic(t *testing.T) {
	policies, pods, namespaces := buildRandomFixture(1)

	want, err := RenderBasic(policies, pods, namespaces)
	if err != nil {
		t.Fatalf("RenderBasic: %v", err)
	}

	for _, workers := range []int{0, 1, 2, 3, 8, 64} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			got, err := RenderParallel(policies, pods, namespaces, workers)
			if err != nil {
				t.Fatalf("RenderParallel: %v", err)
			}
			assertTuples(t, got, want)
		})
	}
}

func TestRenderParallel_Empty(t *testing.T) {
	tuples, err := RenderParallel(nil, nil, nil, 4)
	if err != nil {
		t.Fatalf("RenderParallel: %v", err)
	}
	if len(tuples) != 0 {
		t.Fatalf("expected no tuples, got %+v", tuples)
	}
}

func TestRenderParallel_NamedPortUnsupported(t *testing.T) {
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

	if _, err := RenderParallel([]*networkingv1.NetworkPolicy{np}, pods, nil, 4); err == nil {
		t.Fatal("RenderParallel: expected error for named port, got nil")
	}
}
