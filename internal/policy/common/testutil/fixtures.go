package testutil

import (
	"fmt"
	"math/rand"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildRandomFixture generates a pseudo-random (but deterministic, given
// seed) set of namespaces, pods, and policies large enough to exercise a
// worker-pool renderer's split.
func BuildRandomFixture(seed int64) ([]*networkingv1.NetworkPolicy, []*corev1.Pod, []*corev1.Namespace) {
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

	for n := range numNamespaces {
		nsName := fmt.Sprintf("ns-%d", n)
		namespaces = append(namespaces, Namespace(nsName, map[string]string{"team": teams[r.Intn(len(teams))]}))
		for p := range podsPerNS {
			labels := map[string]string{"app": apps[r.Intn(len(apps))]}
			ip := fmt.Sprintf("10.%d.%d.%d", n, p/256, p%256)
			pods = append(pods, Pod(nsName, fmt.Sprintf("pod-%d", p), ip, labels))
		}
	}

	var policies []*networkingv1.NetworkPolicy
	for i := range numPolicies {
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

// BuildExpressionFixture exercises every metav1.LabelSelector operator
// (Exists, DoesNotExist, In with one and multiple values, NotIn), which
// BuildRandomFixture doesn't touch since it only generates MatchLabels.
// These are exactly the requirement kinds compileSelector splits into
// exact/existsKeys/residual, so this is the fixture that would catch a
// residual-evaluation bug.
func BuildExpressionFixture() ([]*networkingv1.NetworkPolicy, []*corev1.Pod, []*corev1.Namespace) {
	pods := []*corev1.Pod{
		Pod("ns", "a", "10.0.0.1", map[string]string{"app": "web", "tier": "frontend", "canary": "true"}),
		Pod("ns", "b", "10.0.0.2", map[string]string{"app": "web", "tier": "backend"}),
		Pod("ns", "c", "10.0.0.3", map[string]string{"app": "worker", "tier": "backend"}),
		Pod("ns", "d", "10.0.0.4", map[string]string{"app": "web"}), // no tier
		Pod("ns", "e", "10.0.0.5", nil),
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
