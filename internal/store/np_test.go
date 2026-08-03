package store

import (
	"slices"
	"testing"

	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// seq is the iterator form Init takes, over a fixed list of policies.
func seq(policies ...*v1.NetworkPolicy) func(yield func(*v1.NetworkPolicy) bool) {
	return func(yield func(*v1.NetworkPolicy) bool) {
		for _, np := range policies {
			if !yield(np) {
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

func TestNewNetworkPolicies(t *testing.T) {
	nps := NewNetworkPolicies()

	if got := nps.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	// Usable before Init: the relevant-key set is queried by the Pod and
	// Namespace stores, which must not have to care whether Init has run.
	if keys := nps.RelevantKeys(); keys == nil {
		t.Error("RelevantKeys() = nil, want an empty set")
	} else if got := keys.Len(); got != 0 {
		t.Errorf("RelevantKeys().Len() = %d, want 0", got)
	}
	if _, ok := nps.Get("ns1", "np1"); ok {
		t.Error("Get on an empty store = ok, want !ok")
	}
	for range nps.Range("ns1") {
		t.Error("Range on an empty store yielded a policy")
	}
	for range nps.All() {
		t.Error("All on an empty store yielded a policy")
	}
}

func TestInit(t *testing.T) {
	web := policy("ns1", "web", map[string]string{"app": "web"})
	db := policy("ns1", "db", map[string]string{"app": "db"})
	other := policy("ns2", "web", map[string]string{"app": "web"})

	nps := NewNetworkPolicies()
	if err := nps.Init(seq(web, db, other)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if got, want := nps.Len(), 3; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}

	got, ok := nps.Get("ns1", "web")
	if !ok {
		t.Fatal("Get(ns1, web) = !ok, want ok")
	}
	if got.Namespace != "ns1" || got.Name != "web" {
		t.Errorf("Get(ns1, web) = %s/%s, want ns1/web", got.Namespace, got.Name)
	}

	// Same name in two namespaces stays two policies, each indexed under the
	// namespace it came from.
	if got, ok := nps.Get("ns2", "web"); !ok {
		t.Error("Get(ns2, web) = !ok, want ok")
	} else if got.Namespace != "ns2" || got.Name != "web" {
		t.Errorf("Get(ns2, web) = %s/%s, want ns2/web", got.Namespace, got.Name)
	}

	if _, ok := nps.Get("ns1", "nope"); ok {
		t.Error("Get(ns1, nope) = ok, want !ok")
	}
	if _, ok := nps.Get("nope", "web"); ok {
		t.Error("Get(nope, web) = ok, want !ok")
	}
}

func TestInitReplacesPreviousContents(t *testing.T) {
	nps := NewNetworkPolicies()
	if err := nps.Init(seq(policy("ns1", "old", map[string]string{"old": "yes"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := nps.Init(seq(policy("ns2", "new", map[string]string{"new": "yes"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, ok := nps.Get("ns1", "old"); ok {
		t.Error("Get(ns1, old) = ok after re-Init, want the previous contents dropped")
	}
	if _, ok := nps.Get("ns2", "new"); !ok {
		t.Error("Get(ns2, new) = !ok, want ok")
	}
	if got, want := nps.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	// The key set is rebuilt with the contents, not accumulated across Inits.
	if keys := nps.RelevantKeys(); keys.Has("old") || !keys.Has("new") {
		t.Errorf("RelevantKeys() = %v, want [new]", keys.SortedList())
	}
}

func TestInitEmpty(t *testing.T) {
	nps := NewNetworkPolicies()
	if err := nps.Init(seq()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if got := nps.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if keys := nps.RelevantKeys(); keys == nil {
		t.Error("RelevantKeys() = nil, want an empty set")
	} else if got := keys.Len(); got != 0 {
		t.Errorf("RelevantKeys() = %v, want empty", keys.SortedList())
	}
}

func TestInitRelevantKeys(t *testing.T) {
	// Every place a selector can appear, and every operator, in one policy:
	// the podSelector, an ingress peer's podSelector and namespaceSelector,
	// and an egress peer's selectors.
	np := &v1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "np1"},
		Spec: v1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web", "tier": "front"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "exists-key", Operator: metav1.LabelSelectorOpExists},
					// DoesNotExist and NotIn matter as much as the positive
					// operators: an object's labels are projected onto the
					// relevant keys before matching, and a key missing from
					// that projection reads as absent, which satisfies an
					// exclusion the object itself fails.
					{Key: "not-exists-key", Operator: metav1.LabelSelectorOpDoesNotExist},
				},
			},
			Ingress: []v1.NetworkPolicyIngressRule{{
				From: []v1.NetworkPolicyPeer{
					{
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"role": "client"},
						},
						NamespaceSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"prod"}},
							},
						},
					},
					// A peer with no selectors at all contributes nothing.
					{IPBlock: &v1.IPBlock{CIDR: "10.0.0.0/8"}},
				},
			}},
			Egress: []v1.NetworkPolicyEgressRule{{
				To: []v1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "team", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"infra"}},
						},
					},
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"stage": "canary"},
					},
				}},
			}},
		},
	}

	nps := NewNetworkPolicies()
	if err := nps.Init(seq(np, policy("ns2", "np2", map[string]string{"app": "db", "other": "key"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	want := []string{
		"app", "env", "exists-key", "not-exists-key", "other", "role",
		"stage", "team", "tier",
	}
	if got := nps.RelevantKeys().SortedList(); !slices.Equal(got, want) {
		t.Errorf("RelevantKeys() = %v, want %v", got, want)
	}
}

func TestInitRelevantKeysEmptySelector(t *testing.T) {
	// An empty selector matches everything without naming a key, so it makes
	// no key relevant.
	np := &v1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "np1"},
		Spec: v1.NetworkPolicySpec{
			Ingress: []v1.NetworkPolicyIngressRule{{
				From: []v1.NetworkPolicyPeer{{
					PodSelector:       &metav1.LabelSelector{},
					NamespaceSelector: &metav1.LabelSelector{},
				}},
			}},
		},
	}

	nps := NewNetworkPolicies()
	if err := nps.Init(seq(np)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := nps.RelevantKeys().SortedList(); len(got) != 0 {
		t.Errorf("RelevantKeys() = %v, want empty", got)
	}
	if got, want := nps.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

// namedPortPolicy is rejected by repr: resolving a named port needs per-peer
// container port lookups, so conversion errors rather than rendering wrongly.
func namedPortPolicy(ns, name string) *v1.NetworkPolicy {
	np := policy(ns, name, map[string]string{"bad": "yes"})
	np.Spec.Ingress = []v1.NetworkPolicyIngressRule{{
		Ports: []v1.NetworkPolicyPort{
			{Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"}},
		},
	}}
	return np
}

func TestInitUnconvertiblePolicy(t *testing.T) {
	nps := NewNetworkPolicies()
	if err := nps.Init(seq(policy("ns1", "good", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err := nps.Init(seq(
		policy("ns2", "first", map[string]string{"first": "yes"}),
		namedPortPolicy("ns2", "bad"),
		policy("ns2", "last", map[string]string{"last": "yes"}),
	))
	if err == nil {
		t.Fatal("Init with a named port = nil error, want an error")
	}

	// A failed Init leaves the store exactly as it was, rather than holding
	// the policies it managed to convert before hitting the bad one.
	if got, want := nps.Len(), 1; got != want {
		t.Errorf("Len() after a failed Init = %d, want %d", got, want)
	}
	if _, ok := nps.Get("ns1", "good"); !ok {
		t.Error("Get(ns1, good) = !ok after a failed Init, want the previous contents kept")
	}
	if _, ok := nps.Get("ns2", "first"); ok {
		t.Error("Get(ns2, first) = ok, want the partial results of a failed Init discarded")
	}
	if got, want := nps.RelevantKeys().SortedList(), []string{"app"}; !slices.Equal(got, want) {
		t.Errorf("RelevantKeys() after a failed Init = %v, want %v", got, want)
	}
}

func TestInitStopsIteratingOnError(t *testing.T) {
	var yielded int
	counting := func(yield func(*v1.NetworkPolicy) bool) {
		for _, np := range []*v1.NetworkPolicy{
			policy("ns1", "first", nil),
			namedPortPolicy("ns1", "bad"),
			policy("ns1", "last", nil),
		} {
			yielded++
			if !yield(np) {
				return
			}
		}
	}

	nps := NewNetworkPolicies()
	if err := nps.Init(counting); err == nil {
		t.Fatal("Init with a named port = nil error, want an error")
	}
	// The rest of the cache is not walked once the result is known to be
	// discarded.
	if want := 2; yielded != want {
		t.Errorf("policies yielded = %d, want %d", yielded, want)
	}
}

func TestRangeAndAll(t *testing.T) {
	nps := NewNetworkPolicies()
	err := nps.Init(seq(
		policy("ns1", "web", map[string]string{"app": "web"}),
		policy("ns1", "db", map[string]string{"app": "db"}),
		policy("ns2", "web", map[string]string{"app": "web"}),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var inNs1 []string
	for np := range nps.Range("ns1") {
		if np.Namespace != "ns1" {
			t.Errorf("Range(ns1) yielded a policy in %q", np.Namespace)
		}
		inNs1 = append(inNs1, np.Name)
	}
	slices.Sort(inNs1)
	if want := []string{"db", "web"}; !slices.Equal(inNs1, want) {
		t.Errorf("Range(ns1) = %v, want %v", inNs1, want)
	}

	for range nps.Range("nope") {
		t.Error("Range on an unknown namespace yielded a policy")
	}

	var all []string
	for np := range nps.All() {
		all = append(all, np.Namespace+"/"+np.Name)
	}
	slices.Sort(all)
	if want := []string{"ns1/db", "ns1/web", "ns2/web"}; !slices.Equal(all, want) {
		t.Errorf("All() = %v, want %v", all, want)
	}
}

func TestRangeAndAllStopEarly(t *testing.T) {
	nps := NewNetworkPolicies()
	err := nps.Init(seq(
		policy("ns1", "web", nil),
		policy("ns1", "db", nil),
		policy("ns2", "web", nil),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var ranged int
	for range nps.Range("ns1") {
		ranged++
		break
	}
	if want := 1; ranged != want {
		t.Errorf("Range(ns1) with a break yielded %d, want %d", ranged, want)
	}

	var seen int
	for range nps.All() {
		seen++
		break
	}
	if want := 1; seen != want {
		t.Errorf("All() with a break yielded %d, want %d", seen, want)
	}
}
