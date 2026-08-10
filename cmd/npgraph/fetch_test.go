package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func clusterNs(name string, labels map[string]string) corev1.Namespace {
	return corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func clusterPod(ns, name, node string, labels map[string]string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: node},
	}
}

func selector(matchLabels map[string]string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: matchLabels}
}

// testCluster is two namespaces of two pods each, with one ingress policy in
// prod whose peers are the pods labelled role=client in staging.
func testCluster() *Cluster {
	return &Cluster{
		Namespaces: []corev1.Namespace{
			clusterNs("prod", map[string]string{"kubernetes.io/metadata.name": "prod", "tier": "prod"}),
			clusterNs("staging", map[string]string{"kubernetes.io/metadata.name": "staging", "tier": "staging"}),
		},
		Pods: []corev1.Pod{
			clusterPod("prod", "api", "node-a", map[string]string{"app": "api"}),
			clusterPod("prod", "db", "node-b", map[string]string{"app": "db"}),
			clusterPod("staging", "client", "node-a", map[string]string{"role": "client"}),
			clusterPod("staging", "other", "node-b", map[string]string{"role": "other"}),
		},
		Policies: []netv1.NetworkPolicy{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "allow-clients"},
			Spec: netv1.NetworkPolicySpec{
				PodSelector: *selector(map[string]string{"app": "api"}),
				PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
				Ingress: []netv1.NetworkPolicyIngressRule{{
					From: []netv1.NetworkPolicyPeer{{
						NamespaceSelector: selector(map[string]string{"tier": "staging"}),
						PodSelector:       selector(map[string]string{"role": "client"}),
					}},
				}},
			},
		}},
	}
}

func TestGraphFromCluster(t *testing.T) {
	g, err := GraphFromCluster(testCluster())
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}

	if got, want := len(g.Namespaces), 2; got != want {
		t.Errorf("len(Namespaces) = %v, want %v", got, want)
	}
	if got, want := len(g.Pods), 4; got != want {
		t.Errorf("len(Pods) = %v, want %v", got, want)
	}
	if got, want := len(g.Nodes), 2; got != want {
		t.Errorf("len(Nodes) = %v, want %v", got, want)
	}
	if got, want := len(g.Policies), 1; got != want {
		t.Fatalf("len(Policies) = %v, want %v", got, want)
	}

	// Vertex names are namespace-qualified: the graph has one flat namespace of
	// ids while the cluster has one per namespace and kind.
	if got, want := g.Pods[0].Name, "prod/api"; got != want {
		t.Errorf("Pods[0].Name = %q, want %q", got, want)
	}

	pol := g.Policies[0]
	if got, want := pol.Name, "prod/allow-clients#ingress"; got != want {
		t.Errorf("policy name = %q, want %q", got, want)
	}
	if got, want := pol.Direction, DirectionIngress; got != want {
		t.Errorf("policy direction = %q, want %q", got, want)
	}
	if got, want := pol.In, []string{"prod/api"}; !slices.Equal(got, want) {
		t.Errorf("policy In = %v, want %v", got, want)
	}
	if got, want := pol.Out, []string{"staging/client"}; !slices.Equal(got, want) {
		t.Errorf("policy Out = %v, want %v", got, want)
	}
	// The type is not stored on a NetworkPolicy; it is read off the reach of
	// the resolved Out set.
	if got, want := pol.Type, PolicyCrossNSFocused; got != want {
		t.Errorf("policy type = %q, want %q (its peers are in one namespace, and not its own)", got, want)
	}
}

// The distinction repr.selectorToPred preserves and a fetch is most likely to
// get silently wrong: an omitted namespaceSelector means the policy's own
// namespace, an empty one means every namespace.
func TestGraphFromClusterNamespaceSelectorSemantics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peer    netv1.NetworkPolicyPeer
		wantOut []string
	}{
		{
			name:    "omitted namespaceSelector stays in the policy's namespace",
			peer:    netv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}},
			wantOut: []string{"prod/api", "prod/db"},
		},
		{
			name:    "empty namespaceSelector reaches every namespace",
			peer:    netv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: &metav1.LabelSelector{}},
			wantOut: []string{"prod/api", "prod/db", "staging/client", "staging/other"},
		},
		{
			name:    "selected namespace",
			peer:    netv1.NetworkPolicyPeer{NamespaceSelector: selector(map[string]string{"tier": "staging"}), PodSelector: selector(map[string]string{"role": "client"})},
			wantOut: []string{"staging/client"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testCluster()
			c.Policies[0].Spec.Ingress[0].From = []netv1.NetworkPolicyPeer{tc.peer}

			g, err := GraphFromCluster(c)
			if err != nil {
				t.Fatalf("GraphFromCluster() = %v, want nil", err)
			}
			if got := g.Policies[0].Out; !slices.Equal(got, tc.wantOut) {
				t.Errorf("policy Out = %v, want %v", got, tc.wantOut)
			}
		})
	}
}

// A peer with no podSelector selects every pod of its namespaces, which is a
// namespace-level edge rather than a pod-level one -- the difference the graph
// keeps in OutNamespaces.
func TestGraphFromClusterWholeNamespacePeer(t *testing.T) {
	c := testCluster()
	c.Policies[0].Spec.Ingress[0].From = []netv1.NetworkPolicyPeer{{
		NamespaceSelector: selector(map[string]string{"tier": "staging"}),
	}}

	g, err := GraphFromCluster(c)
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}
	pol := g.Policies[0]
	if got, want := pol.OutNamespaces, []string{"staging"}; !slices.Equal(got, want) {
		t.Errorf("policy OutNamespaces = %v, want %v", got, want)
	}
	if got := pol.Out; len(got) != 0 {
		t.Errorf("policy Out = %v, want none", got)
	}
}

// A policy declaring both directions becomes two vertices: the In and Out sets
// of the two directions are independent, and one vertex could not say which
// peer belonged to which.
func TestGraphFromClusterBothDirections(t *testing.T) {
	c := testCluster()
	np := &c.Policies[0]
	np.Spec.PolicyTypes = []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}
	np.Spec.Egress = []netv1.NetworkPolicyEgressRule{{
		To: []netv1.NetworkPolicyPeer{{PodSelector: selector(map[string]string{"app": "db"})}},
	}}

	g, err := GraphFromCluster(c)
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}
	if got, want := len(g.Policies), 2; got != want {
		t.Fatalf("len(Policies) = %v, want %v", got, want)
	}

	byName := g.index().policies
	ingress, ok := byName["prod/allow-clients#ingress"]
	if !ok {
		t.Fatalf("no ingress vertex; got %v", policyNames(g))
	}
	egress, ok := byName["prod/allow-clients#egress"]
	if !ok {
		t.Fatalf("no egress vertex; got %v", policyNames(g))
	}
	if got, want := ingress.Out, []string{"staging/client"}; !slices.Equal(got, want) {
		t.Errorf("ingress Out = %v, want %v", got, want)
	}
	if got, want := egress.Out, []string{"prod/db"}; !slices.Equal(got, want) {
		t.Errorf("egress Out = %v, want %v", got, want)
	}
}

// An omitted policyTypes means ingress, plus egress if the policy has any
// egress rule -- the API's own defaulting, which a collected graph has to
// reproduce or it will describe a policy that does not exist.
func TestGraphFromClusterDefaultPolicyTypes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  netv1.NetworkPolicySpec
		want  []Direction
		rules bool
	}{
		{
			name: "no policyTypes, no rules",
			spec: netv1.NetworkPolicySpec{},
			want: []Direction{DirectionIngress},
		},
		{
			name: "no policyTypes, egress rules",
			spec: netv1.NetworkPolicySpec{Egress: []netv1.NetworkPolicyEgressRule{{}}},
			want: []Direction{DirectionIngress, DirectionEgress},
		},
		{
			name: "explicit egress only",
			spec: netv1.NetworkPolicySpec{PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress}},
			want: []Direction{DirectionEgress},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			np := &netv1.NetworkPolicy{Spec: tc.spec}
			if got := directionsOf(np); !slices.Equal(got, tc.want) {
				t.Errorf("directionsOf() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGraphFromClusterUnscheduledPod(t *testing.T) {
	c := testCluster()
	c.Pods = append(c.Pods, clusterPod("prod", "pending", "", map[string]string{"app": "api"}))

	g, err := GraphFromCluster(c)
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}

	pod, ok := g.index().pods["prod/pending"]
	if !ok {
		t.Fatalf("the unscheduled pod was dropped; a pod with no node is still a policy subject")
	}
	if got, want := pod.Node, unscheduledNode; got != want {
		t.Errorf("unscheduled pod node = %q, want %q", got, want)
	}
	// It is a subject of the policy that selects app=api, and dropping it would
	// have understated the In set.
	if got, want := g.Policies[0].In, []string{"prod/api", "prod/pending"}; !slices.Equal(got, want) {
		t.Errorf("policy In = %v, want %v", got, want)
	}
}

// A pod or policy whose namespace was not in the listing is a race between two
// list calls, not a broken cluster: dropping it is right, and inventing the
// namespace would invent the labels a namespaceSelector matches against.
func TestGraphFromClusterOrphans(t *testing.T) {
	c := testCluster()
	c.Pods = append(c.Pods, clusterPod("gone", "orphan", "node-a", nil))
	c.Policies = append(c.Policies, netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gone", Name: "orphan"},
	})

	g, err := GraphFromCluster(c)
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}
	if _, ok := g.index().pods["gone/orphan"]; ok {
		t.Error("a pod in an unlisted namespace was kept")
	}
	if got, want := len(g.Policies), 1; got != want {
		t.Errorf("len(Policies) = %v, want %v", got, want)
	}
}

// An IPBlock peer names addresses, and the graph has no vertex for one. It is
// skipped rather than turned into something it is not.
func TestGraphFromClusterIPBlockPeer(t *testing.T) {
	c := testCluster()
	c.Policies[0].Spec.Ingress[0].From = append(c.Policies[0].Spec.Ingress[0].From,
		netv1.NetworkPolicyPeer{IPBlock: &netv1.IPBlock{CIDR: "10.0.0.0/8"}})

	g, err := GraphFromCluster(c)
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}
	if got, want := g.Policies[0].Out, []string{"staging/client"}; !slices.Equal(got, want) {
		t.Errorf("policy Out = %v, want %v", got, want)
	}
}

func TestGraphFromClusterBadSelector(t *testing.T) {
	c := testCluster()
	c.Policies[0].Spec.PodSelector = metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Sideways"}},
	}
	if _, err := GraphFromCluster(c); err == nil {
		t.Error("GraphFromCluster() = nil error for an unparseable selector, want one")
	}
}

// Two fetches of an unchanged cluster have to produce the same file: the
// builder walks maps, so the order is imposed rather than inherited.
func TestGraphFromClusterIsStable(t *testing.T) {
	first, err := GraphFromCluster(testCluster())
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}
	for range 5 {
		next, err := GraphFromCluster(testCluster())
		if err != nil {
			t.Fatalf("GraphFromCluster() = %v, want nil", err)
		}
		if got, want := formatGraph(next), formatGraph(first); got != want {
			t.Fatalf("GraphFromCluster() is not deterministic:\n got %s\nwant %s", got, want)
		}
	}
}

// A fetched graph goes out as DOT and can come back in, which is what makes
// `fetch | generate k8s -graph -` a workflow rather than two unrelated commands.
func TestFetchedGraphRoundTrips(t *testing.T) {
	g, err := GraphFromCluster(testCluster())
	if err != nil {
		t.Fatalf("GraphFromCluster() = %v, want nil", err)
	}
	if got := roundTrip(t, g); formatGraph(got) != formatGraph(g) {
		t.Errorf("a fetched graph did not survive the DOT round trip:\n got %s\nwant %s", formatGraph(got), formatGraph(g))
	}
}

func TestFetchCluster(t *testing.T) {
	want := testCluster()
	list := func(kind string) ([]byte, error) {
		switch kind {
		case "namespaces":
			return json.Marshal(corev1.NamespaceList{Items: want.Namespaces})
		case "pods":
			return json.Marshal(corev1.PodList{Items: want.Pods})
		case "networkpolicies":
			return json.Marshal(netv1.NetworkPolicyList{Items: want.Policies})
		}
		return nil, fmt.Errorf("unexpected kind %q", kind)
	}

	got, err := FetchCluster(list)
	if err != nil {
		t.Fatalf("FetchCluster() = %v, want nil", err)
	}
	if got, want := len(got.Namespaces), len(want.Namespaces); got != want {
		t.Errorf("len(Namespaces) = %v, want %v", got, want)
	}
	if got, want := len(got.Pods), len(want.Pods); got != want {
		t.Errorf("len(Pods) = %v, want %v", got, want)
	}
	if got, want := len(got.Policies), len(want.Policies); got != want {
		t.Errorf("len(Policies) = %v, want %v", got, want)
	}
}

func TestFetchClusterErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		list    lister
		wantErr string
	}{
		{
			name:    "list fails",
			list:    func(string) ([]byte, error) { return nil, fmt.Errorf("no such context") },
			wantErr: "no such context",
		},
		{
			name:    "garbage json",
			list:    func(string) ([]byte, error) { return []byte("not json"), nil },
			wantErr: "decoding namespaces",
		},
		{
			name: "garbage pods",
			list: func(kind string) ([]byte, error) {
				if kind == "namespaces" {
					return []byte(`{"items":[]}`), nil
				}
				return []byte("not json"), nil
			},
			wantErr: "decoding pods",
		},
		{
			name: "garbage policies",
			list: func(kind string) ([]byte, error) {
				if kind == "networkpolicies" {
					return []byte("not json"), nil
				}
				return []byte(`{"items":[]}`), nil
			},
			wantErr: "decoding networkpolicies",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FetchCluster(tc.list)
			if err == nil {
				t.Fatalf("FetchCluster() = nil error, want one mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("FetchCluster() = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func policyNames(g *Graph) []string {
	out := make([]string, len(g.Policies))
	for i, p := range g.Policies {
		out[i] = p.Name
	}
	return out
}
