package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// roundTrip is the property the DOT encoding exists for: `generate graph`
// writes a file that `generate k8s -graph` reads back as the same graph.
func roundTrip(t *testing.T, g *Graph) *Graph {
	t.Helper()

	var buf bytes.Buffer
	if err := WriteDOT(&buf, g); err != nil {
		t.Fatalf("WriteDOT() = %v, want nil", err)
	}
	got, err := ParseDOT(&buf)
	if err != nil {
		t.Fatalf("ParseDOT() = %v, want nil\n%s", err, buf.String())
	}
	return got
}

func TestDOTRoundTrip(t *testing.T) {
	want := testGraph()
	// Both peer shapes, so the two kinds of Out edge are exercised.
	want.Policies = append(want.Policies, &Policy{
		Name:          "np-1",
		Namespace:     "ns-1",
		Direction:     DirectionEgress,
		Type:          PolicyGlobal,
		In:            []string{"pod-2", "pod-3"},
		OutNamespaces: []string{"ns-0", "ns-1"},
	})

	got := roundTrip(t, want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseDOT(WriteDOT(g)):\n got %s\nwant %s", formatGraph(got), formatGraph(want))
	}
}

// A generated graph is the real input to the round trip, and it carries shapes
// a fixture does not: a thousand pods, every policy type, empty Out sets.
func TestDOTRoundTripGenerated(t *testing.T) {
	params := DefaultParams()
	params.Pods = 200
	params.Policies = 40
	want, err := Generate(params, 7)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	got := roundTrip(t, want)
	if !reflect.DeepEqual(got, want) {
		t.Error("ParseDOT(WriteDOT(g)) differs from g")
	}
}

// Names that are not DOT identifiers are what a fetched graph is made of:
// "prod/api#ingress" has a '/', a '#' and a '-' in it.
func TestDOTQuotedNames(t *testing.T) {
	want := &Graph{
		Nodes:      []*Node{{Name: "node one"}},
		Namespaces: []*Namespace{{Name: "prod"}},
		Pods:       []*Pod{{Name: `prod/api"x`, Namespace: "prod", Node: "node one"}},
		Policies: []*Policy{{
			Name:      "prod/allow-db#ingress",
			Namespace: "prod",
			Direction: DirectionIngress,
			Type:      PolicySameNS,
			In:        []string{`prod/api"x`},
			Out:       []string{`prod/api"x`},
		}},
	}

	got := roundTrip(t, want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseDOT(WriteDOT(g)):\n got %s\nwant %s", formatGraph(got), formatGraph(want))
	}
}

// A graph written by hand carries the edges and none of the optional
// attributes: a pod's placement is the edge into it, a policy's namespace
// follows from its In set, and its type follows from the reach of its Out set.
// This is the doc's example graph, with the kinds it has to declare.
func TestParseDOTHandWritten(t *testing.T) {
	const src = `digraph {
  // Nodes
  Node1 [kind=node]; Node2 [kind=node]
  Ns1 [kind=namespace]; Ns2 [kind=namespace]
  Pod1 [kind=pod]; Pod2 [kind=pod]; Pod3 [kind=pod]
  NP1 [kind=netpol, direction=ingress]
  NP2 [kind=netpol, direction=egress]

  Node1 -> Pod1
  Node1 -> Pod2
  Node2 -> Pod3

  Ns1 -> Pod1
  Ns2 -> Pod2
  Ns2 -> Pod3

  Pod1 -> NP1 // In
  NP1 -> Pod2 // Out
  NP1 -> Pod3 // Out

  Pod2 -> NP2 // In
  Pod3 -> NP2 // In
  NP2 -> Pod1 // Out
  NP2 -> Ns1  // Out, selecting a namespace.
}`

	g, err := ParseDOT(strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParseDOT() = %v, want nil", err)
	}

	if got, want := len(g.Pods), 3; got != want {
		t.Fatalf("len(Pods) = %v, want %v", got, want)
	}
	pods := g.index().pods
	if got, want := pods["Pod1"].Namespace, "Ns1"; got != want {
		t.Errorf("Pod1.Namespace = %q, want %q", got, want)
	}
	if got, want := pods["Pod3"].Node, "Node2"; got != want {
		t.Errorf("Pod3.Node = %q, want %q", got, want)
	}

	policies := g.index().policies
	np1, np2 := policies["NP1"], policies["NP2"]
	if got, want := np1.Namespace, "Ns1"; got != want {
		t.Errorf("NP1.Namespace = %q, want %q", got, want)
	}
	if got, want := strings.Join(np1.Out, ","), "Pod2,Pod3"; got != want {
		t.Errorf("NP1.Out = %q, want %q", got, want)
	}
	if got, want := np1.Type, PolicyCrossNSFocused; got != want {
		t.Errorf("NP1.Type = %q, want %q (its Out set reaches Ns2 as well as Ns1)", got, want)
	}
	if got, want := np2.Direction, DirectionEgress; got != want {
		t.Errorf("NP2.Direction = %q, want %q", got, want)
	}
	if got, want := strings.Join(np2.OutNamespaces, ","), "Ns1"; got != want {
		t.Errorf("NP2.OutNamespaces = %q, want %q", got, want)
	}
	if got, want := np2.Namespace, "Ns2"; got != want {
		t.Errorf("NP2.Namespace = %q, want %q", got, want)
	}
}

func TestParseDOTErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     string
		wantErr string
	}{
		{name: "not a graph", src: `"a" -> "b"`, wantErr: "no '{' found"},
		{name: "unbalanced braces", src: `digraph { "a" [kind=node]`, wantErr: "unbalanced '{'"},
		{name: "stray close brace", src: `digraph { } }`, wantErr: "unbalanced '}'"},
		{name: "subgraph", src: `digraph { subgraph cluster { "a" [kind=node] } }`, wantErr: "subgraphs are not supported"},
		{name: "unterminated string", src: `digraph { "a [kind=node] }`, wantErr: "unterminated"},
		{name: "unterminated block comment", src: `digraph { /* hello }`, wantErr: "unterminated block comment"},
		{name: "no kind", src: `digraph { "a" [color=red] }`, wantErr: "no kind attribute"},
		{name: "unknown kind", src: `digraph { "a" [kind=service] }`, wantErr: "unknown kind"},
		{name: "duplicate vertex", src: `digraph { "a" [kind=node]; "a" [kind=pod] }`, wantErr: "duplicate declaration"},
		{
			name:    "undeclared endpoint",
			src:     `digraph { "a" [kind=node]; "a" -> "b" }`,
			wantErr: `"b" is not declared`,
		},
		{
			// Node -> Namespace is not a relationship the resource graph has,
			// and quietly ignoring it would lose whatever the writer meant.
			name:    "impossible edge",
			src:     `digraph { "a" [kind=node]; "b" [kind=namespace]; "a" -> "b" }`,
			wantErr: "not a relationship",
		},
		{
			name:    "edge chain",
			src:     `digraph { "a" [kind=node]; "b" [kind=pod]; "c" [kind=netpol]; "a" -> "b" -> "c" }`,
			wantErr: "edge chains are not supported",
		},
		{
			name:    "undirected edge",
			src:     `digraph { "a" [kind=node]; "b" [kind=pod]; "a" -- "b" }`,
			wantErr: "undirected edges are not supported",
		},
		{name: "malformed attribute", src: `digraph { "a" [kind] }`, wantErr: "not a key=value pair"},
		{name: "unterminated attributes", src: `digraph { "a" [kind=node }`, wantErr: "unterminated attribute list"},
		{
			// The attribute and the edge disagree about where the pod runs, and
			// there is no way to tell which the writer meant.
			name:    "contradictory placement",
			src:     `digraph { "n" [kind=node]; "m" [kind=node]; "p" [kind=pod, node="n"]; "m" -> "p" }`,
			wantErr: "by attribute but node",
		},
		{
			name:    "contradictory namespace",
			src:     `digraph { "n" [kind=node]; "a" [kind=namespace]; "b" [kind=namespace]; "p" [kind=pod, namespace="a", node="n"]; "b" -> "p" }`,
			wantErr: "by attribute but namespace",
		},
		{
			// Parsing ends in Validate, so a file that is syntactically fine
			// but describes an impossible cluster is still rejected.
			name:    "invalid graph",
			src:     `digraph { "p" [kind=pod, namespace="nope", node="nope"] }`,
			wantErr: "unknown namespace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDOT(strings.NewReader(tc.src))
			if err == nil {
				t.Fatalf("ParseDOT() = nil error, want one mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseDOT() = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// Comments are stripped whether they trail a statement or stand alone, and a
// '/' inside a quoted name is not the start of one.
func TestParseDOTComments(t *testing.T) {
	const src = `digraph npgraph {
  /* a block
     comment */
  "node-0" [kind=node]; // trailing
  "ns/0" [kind=namespace]
  // standalone
  "pod//0" [kind=pod, namespace="ns/0", node="node-0"]
}`

	g, err := ParseDOT(strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParseDOT() = %v, want nil", err)
	}
	if got, want := len(g.Pods), 1; got != want {
		t.Fatalf("len(Pods) = %v, want %v", got, want)
	}
	if got, want := g.Pods[0].Name, "pod//0"; got != want {
		t.Errorf("Pods[0].Name = %q, want %q", got, want)
	}
}

func TestWriteDOTIsStable(t *testing.T) {
	g := testGraph()

	var first, second bytes.Buffer
	if err := WriteDOT(&first, g); err != nil {
		t.Fatalf("WriteDOT() = %v, want nil", err)
	}
	if err := WriteDOT(&second, g); err != nil {
		t.Fatalf("WriteDOT() = %v, want nil", err)
	}
	if first.String() != second.String() {
		t.Error("WriteDOT() is not deterministic for the same graph")
	}

	// The metadata has to be in attributes and not in comments: everything
	// downstream of a graphviz round trip would lose it otherwise.
	if !strings.Contains(first.String(), `"pod-0" [kind="pod", namespace="ns-0", node="node-0"]`) {
		t.Errorf("WriteDOT() lost the pod attributes:\n%s", first.String())
	}
}

func TestSortGraph(t *testing.T) {
	g := &Graph{
		Nodes:      []*Node{{Name: "node-1"}, {Name: "node-0"}},
		Namespaces: []*Namespace{{Name: "ns-1"}, {Name: "ns-0"}},
		Pods: []*Pod{
			{Name: "pod-1", Namespace: "ns-1", Node: "node-1"},
			{Name: "pod-0", Namespace: "ns-0", Node: "node-0"},
		},
		Policies: []*Policy{
			{Name: "np-1", Namespace: "ns-1", In: []string{"pod-1"}},
			{Name: "np-0", Namespace: "ns-0", Out: []string{"pod-1", "pod-0"}, OutNamespaces: []string{"ns-1", "ns-0"}},
		},
	}
	sortGraph(g)

	if got, want := g.Nodes[0].Name, "node-0"; got != want {
		t.Errorf("Nodes[0] = %q, want %q", got, want)
	}
	if got, want := g.Namespaces[0].Name, "ns-0"; got != want {
		t.Errorf("Namespaces[0] = %q, want %q", got, want)
	}
	if got, want := g.Pods[0].Name, "pod-0"; got != want {
		t.Errorf("Pods[0] = %q, want %q", got, want)
	}
	if got, want := g.Policies[0].Name, "np-0"; got != want {
		t.Errorf("Policies[0] = %q, want %q", got, want)
	}
	if got, want := strings.Join(g.Policies[0].Out, ","), "pod-0,pod-1"; got != want {
		t.Errorf("Policies[0].Out = %q, want %q", got, want)
	}
	if got, want := strings.Join(g.Policies[0].OutNamespaces, ","), "ns-0,ns-1"; got != want {
		t.Errorf("Policies[0].OutNamespaces = %q, want %q", got, want)
	}
}

// formatGraph renders a graph for a failure message. reflect.DeepEqual on a
// graph of pointers reports "not equal" and nothing else, and the DOT is the
// readable form of the thing being compared.
func formatGraph(g *Graph) string {
	var buf bytes.Buffer
	if err := WriteDOT(&buf, g); err != nil {
		return err.Error()
	}
	return buf.String()
}
