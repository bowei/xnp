package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// smallParams is a cluster big enough to exercise every policy type -- broad
// needs more than FocusedNamespaceLimit namespaces to mean anything -- and
// small enough that a failure can be read.
func smallParams() *Params {
	p := DefaultParams()
	p.Pods = 300
	p.PodsPerNamespace = Constant(10) // 30 namespaces
	p.PodsPerNode = Constant(15)      // 20 nodes
	p.Policies = 60
	return p
}

func TestGenerateIsDeterministic(t *testing.T) {
	p := smallParams()

	first, err := Generate(p, 42)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	second, err := Generate(p, 42)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	if got, want := formatGraph(first), formatGraph(second); got != want {
		t.Error("Generate() with the same params and seed produced different graphs")
	}

	other, err := Generate(p, 43)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	if formatGraph(first) == formatGraph(other) {
		t.Error("Generate() with different seeds produced the same graph, so the seed does nothing")
	}
}

// The placement draws and the policy draws come from separate streams, so that
// changing a placement parameter does not reshuffle every policy. Without it
// the two effects cannot be told apart across a series of experiments.
func TestGenerateStreamsAreIndependent(t *testing.T) {
	p := smallParams()
	base, err := Generate(p, 5)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	p2 := smallParams()
	p2.PodsPerNode = Constant(7)
	changed, err := Generate(p2, 5)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	for i, pol := range base.Policies {
		other := changed.Policies[i]
		if pol.Type != other.Type || pol.Direction != other.Direction || pol.Namespace != other.Namespace {
			t.Fatalf("policy %d changed from (%s, %s, %s) to (%s, %s, %s) when only the node size changed",
				i, pol.Type, pol.Direction, pol.Namespace, other.Type, other.Direction, other.Namespace)
		}
	}
	if slices.Equal(nodeNames(base), nodeNames(changed)) {
		t.Error("changing PodsPerNode did not change the node layout")
	}
}

func nodeNames(g *Graph) []string {
	out := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		out[i] = n.Name
	}
	return out
}

func TestGeneratePlacement(t *testing.T) {
	p := smallParams()
	g, err := Generate(p, 1)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	if got, want := len(g.Pods), p.Pods; got != want {
		t.Errorf("len(Pods) = %v, want %v", got, want)
	}
	if got, want := len(g.Namespaces), 30; got != want {
		t.Errorf("len(Namespaces) = %v, want %v (300 pods at 10 per namespace)", got, want)
	}
	if got, want := len(g.Nodes), 20; got != want {
		t.Errorf("len(Nodes) = %v, want %v (300 pods at 15 per node)", got, want)
	}

	perNs := map[string]int{}
	perNode := map[string]int{}
	for _, pod := range g.Pods {
		perNs[pod.Namespace]++
		perNode[pod.Node]++
	}
	for ns, n := range perNs {
		if n != 10 {
			t.Errorf("namespace %q holds %d pods, want 10", ns, n)
		}
	}
	for node, n := range perNode {
		if n != 15 {
			t.Errorf("node %q holds %d pods, want 15", node, n)
		}
	}

	// A node holding a whole namespace would make "same node" and "same
	// namespace" the same fact, and a scale test built on that graph would
	// measure the wrong thing.
	nsOfNode := map[string]map[string]bool{}
	for _, pod := range g.Pods {
		if nsOfNode[pod.Node] == nil {
			nsOfNode[pod.Node] = map[string]bool{}
		}
		nsOfNode[pod.Node][pod.Namespace] = true
	}
	for node, namespaces := range nsOfNode {
		if len(namespaces) < 2 {
			t.Errorf("node %q holds pods from only %d namespace(s), want the placements to be independent", node, len(namespaces))
		}
	}
}

// The last namespace and the last node take whatever pods are left over, so a
// pod count that is not a multiple of the sizes still places every pod exactly
// once.
func TestGeneratePlacementRemainder(t *testing.T) {
	p := smallParams()
	p.Pods = 25
	p.PodsPerNamespace = Constant(10)
	p.PodsPerNode = Constant(10)
	p.Policies = 0

	g, err := Generate(p, 1)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	if got, want := len(g.Namespaces), 3; got != want {
		t.Errorf("len(Namespaces) = %v, want %v", got, want)
	}
	if got, want := len(g.Nodes), 3; got != want {
		t.Errorf("len(Nodes) = %v, want %v", got, want)
	}

	placed := map[string]bool{}
	for _, pod := range g.Pods {
		if pod.Namespace == "" || pod.Node == "" {
			t.Errorf("pod %q is unplaced: namespace %q, node %q", pod.Name, pod.Namespace, pod.Node)
		}
		placed[pod.Name] = true
	}
	if got, want := len(placed), 25; got != want {
		t.Errorf("%d distinct pods placed, want %v", got, want)
	}
}

func TestGeneratePolicyMix(t *testing.T) {
	p := smallParams()
	p.Policies = 100
	p.Mix = PolicyMix{SameNS: 0.5, CrossNSFocused: 0.3, CrossNSBroad: 0.15, Global: 0.05}

	g, err := Generate(p, 3)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	if got, want := len(g.Policies), 100; got != want {
		t.Fatalf("len(Policies) = %v, want %v", got, want)
	}

	counts := map[PolicyType]int{}
	for _, pol := range g.Policies {
		counts[pol.Type]++
	}
	// The mix is allocated rather than sampled, so these are exact: a run whose
	// mix is only approximately right is not the experiment that was asked for.
	for _, tc := range []struct {
		t    PolicyType
		want int
	}{
		{PolicySameNS, 50},
		{PolicyCrossNSFocused, 30},
		{PolicyCrossNSBroad, 15},
		{PolicyGlobal, 5},
	} {
		if got := counts[tc.t]; got != tc.want {
			t.Errorf("%d policies of type %s, want %d", got, tc.t, tc.want)
		}
	}

	// Type is shuffled across the policy list, so a prefix of the policies is
	// still a mix.
	firstTen := map[PolicyType]bool{}
	for _, pol := range g.Policies[:10] {
		firstTen[pol.Type] = true
	}
	if len(firstTen) < 2 {
		t.Errorf("the first ten policies are all of type %v, want the types shuffled across the list", firstTen)
	}
}

func TestGeneratePolicyShape(t *testing.T) {
	p := smallParams()
	g, err := Generate(p, 11)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	podNs := map[string]string{}
	for _, pod := range g.Pods {
		podNs[pod.Name] = pod.Namespace
	}

	seen := map[PolicyType]int{}
	for _, pol := range g.Policies {
		seen[pol.Type]++

		if len(pol.In) == 0 {
			t.Errorf("policy %q has an empty In set; a policy that applies to nothing is not a test case", pol.Name)
		}
		if got := len(slices.Compact(slices.Sorted(slices.Values(pol.In)))); got != len(pol.In) {
			t.Errorf("policy %q has repeated pods in its In set", pol.Name)
		}
		if got := len(slices.Compact(slices.Sorted(slices.Values(pol.Out)))); got != len(pol.Out) {
			t.Errorf("policy %q has repeated pods in its Out set", pol.Name)
		}
		for _, name := range pol.In {
			if podNs[name] != pol.Namespace {
				t.Errorf("policy %q in namespace %q selects pod %q in namespace %q", pol.Name, pol.Namespace, name, podNs[name])
			}
		}

		// A whole-namespace peer set and a pod-level one are alternatives; a
		// policy carrying both would be two experiments at once.
		if len(pol.Out) > 0 && len(pol.OutNamespaces) > 0 {
			t.Errorf("policy %q has both pod peers and namespace peers", pol.Name)
		}

		span := g.OutNamespaceSpan(pol)
		switch pol.Type {
		case PolicySameNS:
			for ns := range g.OutNamespaces(pol) {
				if ns != pol.Namespace {
					t.Errorf("sameNS policy %q reaches namespace %q, want only %q", pol.Name, ns, pol.Namespace)
				}
			}
		case PolicyCrossNSFocused:
			if span > FocusedNamespaceLimit {
				t.Errorf("crossNSFocused policy %q spans %d namespaces, want at most %d", pol.Name, span, FocusedNamespaceLimit)
			}
		case PolicyCrossNSBroad:
			// The out-degree caps how many namespaces pod-level peers can
			// reach, so the claim is about the targets the generator chose, not
			// about the peers that fit inside the degree.
			if len(pol.OutNamespaces) > 0 && len(pol.OutNamespaces) <= FocusedNamespaceLimit {
				t.Errorf("crossNSBroad policy %q targets %d namespaces, want more than %d", pol.Name, len(pol.OutNamespaces), FocusedNamespaceLimit)
			}
		case PolicyGlobal:
			if len(pol.OutNamespaces) > 0 && len(pol.OutNamespaces) != len(g.Namespaces) {
				t.Errorf("global policy %q targets %d of %d namespaces, want all of them", pol.Name, len(pol.OutNamespaces), len(g.Namespaces))
			}
		}
	}

	for _, want := range policyTypes {
		if seen[want] == 0 {
			t.Errorf("no policies of type %s were generated, so its shape went untested", want)
		}
	}
}

// The whole-namespace peer shape has to actually appear when it is asked for,
// and has to disappear when it is not: it is the difference between one peer
// standing for a thousand pods and a thousand peers.
func TestGenerateNamespacePeers(t *testing.T) {
	p := smallParams()
	p.NamespacePeerFraction = 1

	g, err := Generate(p, 2)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	for _, pol := range g.Policies {
		if len(pol.OutNamespaces) == 0 {
			t.Fatalf("policy %q has no namespace peers at namespacePeerFraction 1", pol.Name)
		}
		if len(pol.Out) > 0 {
			t.Fatalf("policy %q has pod peers at namespacePeerFraction 1", pol.Name)
		}
	}

	p.NamespacePeerFraction = 0
	g, err = Generate(p, 2)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	for _, pol := range g.Policies {
		if len(pol.OutNamespaces) > 0 {
			t.Fatalf("policy %q has namespace peers at namespacePeerFraction 0", pol.Name)
		}
	}
}

func TestGenerateDirectionFraction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fraction float64
		want     Direction
	}{
		{name: "all ingress", fraction: 1, want: DirectionIngress},
		{name: "all egress", fraction: 0, want: DirectionEgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := smallParams()
			p.IngressFraction = tc.fraction
			g, err := Generate(p, 4)
			if err != nil {
				t.Fatalf("Generate() = %v, want nil", err)
			}
			for _, pol := range g.Policies {
				if pol.Direction != tc.want {
					t.Fatalf("policy %q has direction %q, want %q", pol.Name, pol.Direction, tc.want)
				}
			}
		})
	}

	p := smallParams()
	p.IngressFraction = 0.5
	g, err := Generate(p, 4)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	dirs := map[Direction]int{}
	for _, pol := range g.Policies {
		dirs[pol.Direction]++
	}
	if dirs[DirectionIngress] == 0 || dirs[DirectionEgress] == 0 {
		t.Errorf("directions = %v, want both at ingressFraction 0.5", dirs)
	}
}

// Degrees are drawn from distributions that can name numbers the graph cannot
// supply. Clamping is the answer, and the boundary cases are where it shows: a
// namespace of ten pods against an in-degree of a thousand.
func TestGenerateDegreeClamping(t *testing.T) {
	p := smallParams()
	p.InDegree = Constant(1000)
	p.OutDegree = Constant(1000)
	p.Mix = PolicyMix{SameNS: 1}
	p.NamespacePeerFraction = 0

	g, err := Generate(p, 6)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	for _, pol := range g.Policies {
		if got, want := len(pol.In), 10; got != want {
			t.Errorf("policy %q selects %d pods, want %d (the size of its namespace)", pol.Name, got, want)
		}
		if got, want := len(pol.Out), 10; got != want {
			t.Errorf("policy %q targets %d pods, want %d (the size of its namespace)", pol.Name, got, want)
		}
	}

	// A negative draw is a legitimate way to say "usually none": it must not
	// become a negative count or an error.
	p.OutDegree = Constant(-5)
	g, err = Generate(p, 6)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	for _, pol := range g.Policies {
		if len(pol.Out) != 0 {
			t.Errorf("policy %q targets %d pods at out-degree -5, want none", pol.Name, len(pol.Out))
		}
	}
}

// A cluster with fewer namespaces than CrossNSBroad wants is a legitimate
// experiment -- it is the parameter the user varied -- so the span clamps to
// the cluster rather than failing.
func TestGenerateBroadOnSmallCluster(t *testing.T) {
	p := smallParams()
	p.Pods = 20
	p.PodsPerNamespace = Constant(10) // 2 namespaces, well under the broad floor
	p.Policies = 10
	p.Mix = PolicyMix{CrossNSBroad: 1}
	p.NamespacePeerFraction = 1

	g, err := Generate(p, 8)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	for _, pol := range g.Policies {
		if got, want := len(pol.OutNamespaces), 2; got != want {
			t.Errorf("policy %q targets %d namespaces, want %d (every namespace there is)", pol.Name, got, want)
		}
	}
}

func TestGenerateEdgeCases(t *testing.T) {
	t.Run("single pod", func(t *testing.T) {
		p := smallParams()
		p.Pods = 1
		p.Policies = 5
		g, err := Generate(p, 9)
		if err != nil {
			t.Fatalf("Generate() = %v, want nil", err)
		}
		if got, want := len(g.Pods), 1; got != want {
			t.Errorf("len(Pods) = %v, want %v", got, want)
		}
		if got, want := len(g.Policies), 5; got != want {
			t.Errorf("len(Policies) = %v, want %v", got, want)
		}
	})

	t.Run("no policies", func(t *testing.T) {
		p := smallParams()
		p.Policies = 0
		g, err := Generate(p, 9)
		if err != nil {
			t.Fatalf("Generate() = %v, want nil", err)
		}
		if got, want := len(g.Policies), 0; got != want {
			t.Errorf("len(Policies) = %v, want %v", got, want)
		}
	})

	t.Run("invalid params", func(t *testing.T) {
		p := smallParams()
		p.Pods = 0
		if _, err := Generate(p, 9); err == nil {
			t.Error("Generate() = nil error for pods: 0, want one")
		}
	})
}

// Every generated graph has to satisfy the same invariants a parsed one does;
// Generate checks this itself, and this is the test that the check is real.
func TestGenerateProducesValidGraphs(t *testing.T) {
	p := smallParams()
	for seed := range uint64(10) {
		g, err := Generate(p, seed)
		if err != nil {
			t.Fatalf("Generate(seed=%d) = %v, want nil", seed, err)
		}
		if err := g.Validate(); err != nil {
			t.Errorf("Generate(seed=%d) produced an invalid graph: %v", seed, err)
		}
	}
}

func TestSamplePodsAcross(t *testing.T) {
	podsByNs := map[string][]string{
		"ns-0": {"a0", "a1", "a2"},
		"ns-1": {"b0", "b1"},
		"ns-2": {"c0"},
	}
	targets := []string{"ns-0", "ns-1", "ns-2"}

	t.Run("spreads before repeating", func(t *testing.T) {
		r := testRand()
		got := samplePodsAcross(r, podsByNs, targets, 3)
		if len(got) != 3 {
			t.Fatalf("samplePodsAcross(n=3) returned %d pods, want 3", len(got))
		}
		// Three pods over three targets must be one from each: a policy typed
		// by how many namespaces it reaches has to actually reach them.
		ns := map[string]bool{}
		for _, name := range got {
			ns[name[:1]] = true
		}
		if len(ns) != 3 {
			t.Errorf("samplePodsAcross(n=3) = %v, want one pod from each namespace", got)
		}
	})

	t.Run("exhausted targets", func(t *testing.T) {
		r := testRand()
		got := samplePodsAcross(r, podsByNs, targets, 100)
		if want := 6; len(got) != want {
			t.Errorf("samplePodsAcross(n=100) returned %d pods, want %d (every pod there is)", len(got), want)
		}
		if got := len(slices.Compact(slices.Sorted(slices.Values(got)))); got != 6 {
			t.Errorf("samplePodsAcross() returned repeated pods")
		}
	})

	t.Run("degenerate", func(t *testing.T) {
		r := testRand()
		if got := samplePodsAcross(r, podsByNs, targets, 0); got != nil {
			t.Errorf("samplePodsAcross(n=0) = %v, want nil", got)
		}
		if got := samplePodsAcross(r, podsByNs, nil, 5); got != nil {
			t.Errorf("samplePodsAcross(no targets) = %v, want nil", got)
		}
		if got := samplePodsAcross(r, podsByNs, []string{"ns-empty"}, 5); len(got) != 0 {
			t.Errorf("samplePodsAcross(empty namespace) = %v, want nothing", got)
		}
	})
}

func TestSampleN(t *testing.T) {
	from := []string{"a", "b", "c", "d"}
	r := testRand()

	if got := sampleN(r, from, 0); got != nil {
		t.Errorf("sampleN(n=0) = %v, want nil", got)
	}
	if got := sampleN(r, nil, 3); got != nil {
		t.Errorf("sampleN(empty) = %v, want nil", got)
	}
	if got, want := len(sampleN(r, from, 10)), len(from); got != want {
		t.Errorf("len(sampleN(n=10)) = %v, want %v (the whole set)", got, want)
	}

	for range 20 {
		got := sampleN(r, from, 2)
		if len(got) != 2 {
			t.Fatalf("sampleN(n=2) = %v, want 2 elements", got)
		}
		if got[0] == got[1] {
			t.Fatalf("sampleN(n=2) = %v, want distinct elements", got)
		}
	}
}

// The DOT of a generated graph is what the tool actually hands over, so it is
// worth one end-to-end look at its shape.
func TestGenerateWritesReadableDOT(t *testing.T) {
	p := smallParams()
	p.Pods = 4
	p.PodsPerNamespace = Constant(2)
	p.PodsPerNode = Constant(4)
	p.Policies = 1
	p.Mix = PolicyMix{SameNS: 1}
	p.NamespacePeerFraction = 0

	g, err := Generate(p, 1)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	var buf bytes.Buffer
	if err := WriteDOT(&buf, g); err != nil {
		t.Fatalf("WriteDOT() = %v, want nil", err)
	}
	out := buf.String()
	for _, want := range []string{
		"digraph npgraph {",
		`"node-0" [kind="node"]`,
		`"ns-0" [kind="namespace"]`,
		`"np-0" [kind="netpol"`,
		"// In",
		"// Out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteDOT() output does not contain %q:\n%s", want, out)
		}
	}
}
