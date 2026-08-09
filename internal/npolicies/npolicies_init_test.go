package npolicies

import (
	"fmt"
	"testing"

	"github.com/bowei/xnp/internal/rlabels"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/set"
)

func testPolicy(ns, name string, spec networkingv1.NetworkPolicySpec) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       spec,
	}
}

// testNamespaces builds the Namespace cache Init resolves namespaceSelectors
// against, from ns name -> labels.
func testNamespaces(byName map[string]map[string]string) map[string]*corev1.Namespace {
	out := map[string]*corev1.Namespace{}
	for name, labels := range byName {
		out[name] = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		}
	}
	return out
}

func testSeq(nps ...*networkingv1.NetworkPolicy) func(yield func(*networkingv1.NetworkPolicy) bool) {
	return func(yield func(*networkingv1.NetworkPolicy) bool) {
		for _, np := range nps {
			if !yield(np) {
				return
			}
		}
	}
}

// checkRLabels compares what Init recorded against the per-namespace keys it
// should have. RLabels exposes no accessor, so the comparison goes through
// Difference: a nil Diff is equality, and the Diff itself names what is off.
func checkRLabels(t *testing.T, got *rlabels.RLabels, want map[string][]string) {
	t.Helper()

	var b rlabels.Builder
	for ns, keys := range want {
		b.Merge(ns, set.New(keys...))
	}
	if d := got.Difference(b.Build()); d != nil {
		t.Errorf("rlabels: added %v, removed %v, changed %v (want %v)",
			perNs(d.Added), perNs(d.Removed), perNs(d.Changed), want)
	}
}

// perNs renders a category of a Diff deterministically, so a failure names the
// keys that differ rather than printing them in map order.
func perNs(pls rlabels.PerNsLabels) string {
	out := map[string][]string{}
	for ns, keys := range pls {
		out[ns] = keys.SortedList()
	}
	return fmt.Sprint(out)
}

func checkNames(t *testing.T, nps *NPolicies, want map[string][]string) {
	t.Helper()

	if got, want := len(nps.nps), len(want); got != want {
		t.Errorf("len(nps) = %d namespaces, want %d", got, want)
	}
	for ns, names := range want {
		inNs, ok := nps.nps[ns]
		if !ok {
			t.Errorf("nps has no namespace %q", ns)
			continue
		}
		if got, want := len(inNs), len(names); got != want {
			t.Errorf("len(nps[%q]) = %d, want %d", ns, got, want)
		}
		for _, name := range names {
			e, ok := inNs[name]
			if !ok {
				t.Errorf("nps[%q] has no policy %q", ns, name)
				continue
			}
			if e.Namespace != ns || e.Name != name {
				t.Errorf("nps[%q][%q] = entry %s/%s, want %s/%s", ns, name, e.Namespace, e.Name, ns, name)
			}
		}
	}
}

func TestInit(t *testing.T) {
	np1 := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}},
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			}},
		}},
	})
	// A second policy in the same namespace: its keys join np1's rather than
	// replacing them.
	np2 := testPolicy("ns1", "np2", networkingv1.NetworkPolicySpec{
		Egress: []networkingv1.NetworkPolicyEgressRule{{
			To: []networkingv1.NetworkPolicyPeer{{
				PodSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "team", Operator: metav1.LabelSelectorOpDoesNotExist},
				}},
			}},
		}},
	})
	np3 := testPolicy("ns2", "np1", networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
	})

	namespaces := testNamespaces(map[string]map[string]string{
		"ns1": nil,
		"ns2": {"tier": "prod"},
		"ns3": {"tier": "dev"},
	})

	nps := NewNPolicies()
	if err := nps.Init(namespaces, testSeq(np1, np2, np3)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	checkNames(t, nps, map[string][]string{"ns1": {"np1", "np2"}, "ns2": {"np1"}})
	// team comes from a DoesNotExist expression on a peer with no
	// namespaceSelector and tier from a namespaceSelector: both are relevant,
	// and both are attributed to the namespace of the policy that names them.
	// role is the podSelector of a peer that does carry one, so it lands in ns2
	// -- the only namespace labelled tier=prod -- joining the keys np3 makes
	// relevant there, and not in ns1 or in the unmatched ns3.
	checkRLabels(t, nps.rlabels, map[string][]string{
		"ns1": {"app", "tier", "team"},
		"ns2": {"app", "role"},
	})
}

// A peer's namespaceSelector reaches the policy's own namespace like any other:
// it is matched against the Namespace labels, not treated as "elsewhere".
func TestInit_NamespaceSelectorMatchesOwnNamespace(t *testing.T) {
	np := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}},
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			}},
		}},
	})
	namespaces := testNamespaces(map[string]map[string]string{
		"ns1": {"tier": "prod"},
		"ns2": {"tier": "dev"},
	})

	nps := NewNPolicies()
	if err := nps.Init(namespaces, testSeq(np)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	checkRLabels(t, nps.rlabels, map[string][]string{"ns1": {"role", "tier"}})
}

// An empty namespaceSelector is not an omitted one: it matches every namespace,
// so the peer's podSelector keys are relevant everywhere.
func TestInit_EmptyNamespaceSelector(t *testing.T) {
	np := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		Egress: []networkingv1.NetworkPolicyEgressRule{{
			To: []networkingv1.NetworkPolicyPeer{{
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"role": "server"}},
				NamespaceSelector: &metav1.LabelSelector{},
			}},
		}},
	})
	namespaces := testNamespaces(map[string]map[string]string{
		"ns1": nil,
		"ns2": {"tier": "dev"},
		"ns3": nil,
	})

	nps := NewNPolicies()
	if err := nps.Init(namespaces, testSeq(np)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	checkRLabels(t, nps.rlabels, map[string][]string{
		"ns1": {"role"},
		"ns2": {"role"},
		"ns3": {"role"},
	})
}

// A namespaceSelector no namespace satisfies registers its peer's keys nowhere,
// and a peer with no podSelector -- every pod in the matched namespaces -- has
// no keys to register in the first place.
func TestInit_NamespaceSelectorNoKeys(t *testing.T) {
	unmatched := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}},
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "staging"}},
			}},
		}},
	})
	allPods := testPolicy("ns2", "np2", networkingv1.NetworkPolicySpec{
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{},
			}},
		}},
	})
	namespaces := testNamespaces(map[string]map[string]string{
		"ns1": {"tier": "prod"},
		"ns2": {"tier": "dev"},
	})

	nps := NewNPolicies()
	if err := nps.Init(namespaces, testSeq(unmatched, allPods)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// tier is still relevant in ns1: it is named by a namespaceSelector, and
	// those keys stay with the policy that names them whether or not the
	// selector matches anything.
	checkRLabels(t, nps.rlabels, map[string][]string{"ns1": {"tier"}})
}

// A policy whose selectors name no keys leaves its namespace out of the
// relevant set entirely: projecting every pod there onto the empty label set is
// what an absent namespace already means.
func TestInit_NoKeys(t *testing.T) {
	np := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
	})

	nps := NewNPolicies()
	if err := nps.Init(nil, testSeq(np)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	checkNames(t, nps, map[string][]string{"ns1": {"np1"}})
	checkRLabels(t, nps.rlabels, nil)
}

func TestInit_Empty(t *testing.T) {
	nps := NewNPolicies()
	if err := nps.Init(nil, testSeq()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	checkNames(t, nps, nil)
	checkRLabels(t, nps.rlabels, nil)
}

// A second Init replaces what the store holds rather than merging into it,
// including the relevant labels of policies that are gone.
func TestInit_Replaces(t *testing.T) {
	first := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	})
	second := testPolicy("ns2", "np2", networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "db"}},
	})

	nps := NewNPolicies()
	if err := nps.Init(nil, testSeq(first)); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := nps.Init(nil, testSeq(second)); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	checkNames(t, nps, map[string][]string{"ns2": {"np2"}})
	checkRLabels(t, nps.rlabels, map[string][]string{"ns2": {"role"}})
}

// An unconvertible policy fails the whole call and leaves the store as it was:
// a half-populated store renders as more allowed traffic than the cluster asked
// for.
func TestInit_ConversionErrorLeavesStore(t *testing.T) {
	good := testPolicy("ns1", "np1", networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	})
	bad := testPolicy("ns2", "np2", networkingv1.NetworkPolicySpec{
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{{
				Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"},
			}},
		}},
	})
	after := testPolicy("ns3", "np3", networkingv1.NetworkPolicySpec{})

	nps := NewNPolicies()
	if err := nps.Init(nil, testSeq(good)); err != nil {
		t.Fatalf("first Init: %v", err)
	}

	// after is placed past the failure to check the iterator is stopped rather
	// than run to completion.
	var seen int
	seq := func(yield func(*networkingv1.NetworkPolicy) bool) {
		for _, np := range []*networkingv1.NetworkPolicy{good, bad, after} {
			seen++
			if !yield(np) {
				return
			}
		}
	}
	if err := nps.Init(nil, seq); err == nil {
		t.Fatalf("Init: want an error for a named port, got nil")
	}
	if got, want := seen, 2; got != want {
		t.Errorf("policies converted = %d, want %d (the iterator should stop at the failure)", got, want)
	}

	checkNames(t, nps, map[string][]string{"ns1": {"np1"}})
	checkRLabels(t, nps.rlabels, map[string][]string{"ns1": {"app"}})
}
