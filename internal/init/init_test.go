package init

import (
	"testing"

	"github.com/bowei/xnp/internal/store"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// policySeq, podSeq and nsSeq are the iterator forms Caches takes, over fixed
// lists of objects.
func policySeq(policies ...*v1.NetworkPolicy) func(yield func(*v1.NetworkPolicy) bool) {
	return func(yield func(*v1.NetworkPolicy) bool) {
		for _, np := range policies {
			if !yield(np) {
				return
			}
		}
	}
}

func podSeq(pods ...*corev1.Pod) func(yield func(*corev1.Pod) bool) {
	return func(yield func(*corev1.Pod) bool) {
		for _, p := range pods {
			if !yield(p) {
				return
			}
		}
	}
}

func nsSeq(namespaces ...*corev1.Namespace) func(yield func(*corev1.Namespace) bool) {
	return func(yield func(*corev1.Namespace) bool) {
		for _, ns := range namespaces {
			if !yield(ns) {
				return
			}
		}
	}
}

// policy builds a NetworkPolicy selecting podSelector, with no rules.
func policy(ns, name string, podSelector map[string]string) *v1.NetworkPolicy {
	return &v1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podSelector},
		},
	}
}

// pod builds a Pod with the given labels and a single IP.
func pod(ns, name, ip string, labels map[string]string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
	}
	if ip != "" {
		p.Status.PodIP = ip
		p.Status.PodIPs = []corev1.PodIP{{IP: ip}}
	}
	return p
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func newStores() store.Stores {
	return store.Stores{
		NetworkPolicies: store.NewNetworkPolicies(),
		Pods:            store.NewPods(),
		Namespaces:      store.NewNamespaces(),
	}
}

func TestDo(t *testing.T) {
	stores := newStores()
	err := Do(stores, Caches{
		NetworkPolicies: policySeq(
			policy("ns1", "web", map[string]string{"app": "web"}),
			policy("ns2", "db", map[string]string{"app": "db"}),
		),
		Pods: podSeq(
			pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
			pod("ns1", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
			pod("ns2", "db-1", "10.0.0.3", map[string]string{"app": "db"}),
		),
		Namespaces: nsSeq(
			namespace("ns1", map[string]string{"env": "prod"}),
			namespace("ns2", nil),
		),
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got, want := stores.NetworkPolicies.Len(), 2; got != want {
		t.Errorf("NetworkPolicies.Len() = %d, want %d", got, want)
	}
	if got, want := stores.Pods.Len(), 3; got != want {
		t.Errorf("Pods.Len() = %d, want %d", got, want)
	}
	if got, want := stores.Namespaces.Len(), 2; got != want {
		t.Errorf("Namespaces.Len() = %d, want %d", got, want)
	}
	if got, want := stores.NetworkPolicies.RelevantKeys().Len(), 1; got != want {
		t.Errorf("RelevantKeys().Len() = %d, want %d", got, want)
	}
}

func TestDoOrdersPolicyKeysBeforePods(t *testing.T) {
	// The ordering is the whole reason Do exists rather than three calls at the
	// call site: the pods are grouped by their labels projected onto the keys
	// the policies name, so a Pods.Init that ran before the policies were read
	// would project onto the empty set and collapse these two into one class.
	stores := newStores()
	err := Do(stores, Caches{
		NetworkPolicies: policySeq(policy("ns1", "web", map[string]string{"app": "web"})),
		Pods: podSeq(
			pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
			pod("ns1", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
		),
		Namespaces: nsSeq(namespace("ns1", nil)),
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	var classes int
	for range stores.Pods.Classes().All() {
		classes++
	}
	if want := 2; classes != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", classes, want)
	}
}

func TestDoEmptyCluster(t *testing.T) {
	stores := newStores()
	if err := Do(stores, Caches{
		NetworkPolicies: policySeq(),
		Pods:            podSeq(),
		Namespaces:      nsSeq(),
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := stores.NetworkPolicies.Len() + stores.Pods.Len() + stores.Namespaces.Len(); got != 0 {
		t.Errorf("stores hold %d objects after an empty Do, want 0", got)
	}
}

func TestDoUnconvertiblePolicy(t *testing.T) {
	// A named port cannot be resolved without per-peer-pod container port
	// lookups, so repr rejects it and startup fails rather than rendering a
	// policy the cluster did not ask for.
	named := policy("ns1", "web", map[string]string{"app": "web"})
	named.Spec.Ingress = []v1.NetworkPolicyIngressRule{{
		Ports: []v1.NetworkPolicyPort{{Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"}}},
	}}

	stores := newStores()
	err := Do(stores, Caches{
		NetworkPolicies: policySeq(named),
		Pods:            podSeq(pod("ns1", "web-1", "10.0.0.1", nil)),
		Namespaces:      nsSeq(namespace("ns1", nil)),
	})
	if err == nil {
		t.Fatal("Do with an unrepresentable policy = nil error, want an error")
	}

	// The steps after the failure do not run: the stores are not a consistent
	// view of the cluster, and the caller is expected to fail startup.
	if got := stores.Pods.Len(); got != 0 {
		t.Errorf("Pods.Len() after a failed Do = %d, want 0", got)
	}
	if got := stores.Namespaces.Len(); got != 0 {
		t.Errorf("Namespaces.Len() after a failed Do = %d, want 0", got)
	}
}

func TestDoUnconvertiblePod(t *testing.T) {
	stores := newStores()
	err := Do(stores, Caches{
		NetworkPolicies: policySeq(policy("ns1", "web", map[string]string{"app": "web"})),
		Pods:            podSeq(pod("ns1", "bad", "not-an-ip", nil)),
		Namespaces:      nsSeq(namespace("ns1", nil)),
	})
	if err == nil {
		t.Fatal("Do with an unrepresentable pod = nil error, want an error")
	}
	if got := stores.Namespaces.Len(); got != 0 {
		t.Errorf("Namespaces.Len() after a failed Do = %d, want 0", got)
	}
}
