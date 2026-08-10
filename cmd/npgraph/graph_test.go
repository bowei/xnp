package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// testGraph is the fixture most of these tests work from: two namespaces, two
// nodes, four pods, and one ingress policy reaching across namespaces. It is
// small enough to assert about by hand and shaped like the doc's example.
func testGraph() *Graph {
	return &Graph{
		Nodes: []*Node{{Name: "node-0"}, {Name: "node-1"}},
		Namespaces: []*Namespace{
			{Name: "ns-0"},
			{Name: "ns-1"},
		},
		Pods: []*Pod{
			{Name: "pod-0", Namespace: "ns-0", Node: "node-0"},
			{Name: "pod-1", Namespace: "ns-0", Node: "node-1"},
			{Name: "pod-2", Namespace: "ns-1", Node: "node-0"},
			{Name: "pod-3", Namespace: "ns-1", Node: "node-1"},
		},
		Policies: []*Policy{{
			Name:      "np-0",
			Namespace: "ns-0",
			Direction: DirectionIngress,
			Type:      PolicyCrossNSFocused,
			In:        []string{"pod-0"},
			Out:       []string{"pod-2"},
		}},
	}
}

func TestGraphValidate(t *testing.T) {
	if err := testGraph().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(g *Graph)
		wantErr string
	}{
		{
			name:    "unknown namespace",
			mutate:  func(g *Graph) { g.Pods[0].Namespace = "ns-9" },
			wantErr: "unknown namespace",
		},
		{
			name:    "unknown node",
			mutate:  func(g *Graph) { g.Pods[0].Node = "node-9" },
			wantErr: "unknown node",
		},
		{
			// The vertex ids of a DOT file are one flat namespace, so a pod and
			// a node with the same name would come back as one vertex.
			name:    "name reused across kinds",
			mutate:  func(g *Graph) { g.Pods[0].Name = "node-0" },
			wantErr: "declared as both",
		},
		{
			name:    "duplicate pod",
			mutate:  func(g *Graph) { g.Pods = append(g.Pods, g.Pods[0]) },
			wantErr: "duplicate vertex",
		},
		{
			name:    "empty name",
			mutate:  func(g *Graph) { g.Namespaces[0].Name = "" },
			wantErr: "empty name",
		},
		{
			// A podSelector cannot reach out of its own namespace, so an In set
			// that does describes nothing a NetworkPolicy could express.
			name:    "In set crosses namespaces",
			mutate:  func(g *Graph) { g.Policies[0].In = append(g.Policies[0].In, "pod-2") },
			wantErr: "selects pod",
		},
		{
			name:    "unknown In pod",
			mutate:  func(g *Graph) { g.Policies[0].In = []string{"pod-9"} },
			wantErr: "unknown pod",
		},
		{
			name:    "unknown Out pod",
			mutate:  func(g *Graph) { g.Policies[0].Out = []string{"pod-9"} },
			wantErr: "unknown pod",
		},
		{
			name:    "unknown Out namespace",
			mutate:  func(g *Graph) { g.Policies[0].OutNamespaces = []string{"ns-9"} },
			wantErr: "unknown namespace",
		},
		{
			name:    "unknown policy namespace",
			mutate:  func(g *Graph) { g.Policies[0].Namespace = "ns-9"; g.Policies[0].In = nil },
			wantErr: "unknown namespace",
		},
		{
			name:    "bad direction",
			mutate:  func(g *Graph) { g.Policies[0].Direction = "sideways" },
			wantErr: "invalid direction",
		},
		{
			name:    "bad type",
			mutate:  func(g *Graph) { g.Policies[0].Type = "enormous" },
			wantErr: "invalid type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := testGraph()
			tc.mutate(g)
			err := g.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil error, want one mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestPodsByNamespace(t *testing.T) {
	got := testGraph().PodsByNamespace()
	want := map[string][]string{
		"ns-0": {"pod-0", "pod-1"},
		"ns-1": {"pod-2", "pod-3"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("PodsByNamespace() = %v, want %v", got, want)
	}
}

func TestOutNamespaceSpan(t *testing.T) {
	g := testGraph()

	for _, tc := range []struct {
		name string
		pol  Policy
		want int
	}{
		{name: "no out", pol: Policy{}, want: 0},
		{name: "one namespace", pol: Policy{Out: []string{"pod-2", "pod-3"}}, want: 1},
		{name: "two namespaces", pol: Policy{Out: []string{"pod-0", "pod-2"}}, want: 2},
		// A namespace reached by a whole-namespace peer counts the same as one
		// reached through its pods; the span is a claim about reach.
		{name: "namespace peers", pol: Policy{OutNamespaces: []string{"ns-0", "ns-1"}}, want: 2},
		{name: "overlapping", pol: Policy{Out: []string{"pod-0"}, OutNamespaces: []string{"ns-0"}}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.OutNamespaceSpan(&tc.pol); got != tc.want {
				t.Errorf("OutNamespaceSpan() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyPolicy(t *testing.T) {
	// A cluster wide enough that "broad" and "global" are distinguishable:
	// twelve namespaces, one pod each.
	g := &Graph{Nodes: []*Node{{Name: "node-0"}}}
	for i := range 12 {
		ns := nsName(i)
		g.Namespaces = append(g.Namespaces, &Namespace{Name: ns})
		g.Pods = append(g.Pods, &Pod{Name: podName(i), Namespace: ns, Node: "node-0"})
	}

	pods := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, podName(i))
		}
		return out
	}

	for _, tc := range []struct {
		name string
		pol  Policy
		want PolicyType
	}{
		{name: "same namespace", pol: Policy{Out: []string{"pod-0"}}, want: PolicySameNS},
		{name: "no out at all", pol: Policy{}, want: PolicySameNS},
		// One namespace, but not the policy's own: as cross-namespace as five,
		// and nothing about it is same-namespace.
		{name: "one other namespace", pol: Policy{Out: []string{"pod-1"}}, want: PolicyCrossNSFocused},
		{name: "focused", pol: Policy{Out: pods(4)}, want: PolicyCrossNSFocused},
		{name: "at the focused limit", pol: Policy{Out: pods(FocusedNamespaceLimit)}, want: PolicyCrossNSFocused},
		{name: "past the focused limit", pol: Policy{Out: pods(FocusedNamespaceLimit + 1)}, want: PolicyCrossNSBroad},
		{name: "every namespace", pol: Policy{Out: pods(12)}, want: PolicyGlobal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every case is a policy living in ns-0, since SameNS is a claim
			// about the policy's own namespace and not only about the count.
			tc.pol.Namespace = nsName(0)
			if got := classifyPolicy(g, &tc.pol); got != tc.want {
				t.Errorf("classifyPolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}
