package main

import (
	"fmt"
	"math/rand/v2"
)

// Generate builds a graph from p. Two calls with the same params and seed
// produce identical graphs -- that is the whole contract of the tool, since an
// experiment is quoted as a params file and a seed rather than by shipping the
// graph around.
func Generate(p *Params, seed uint64) (*Graph, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	// Two independent streams from one seed. The alternative, drawing
	// everything from one stream, makes the policies depend on how many draws
	// the placement happened to consume, so raising PodsPerNamespace by one
	// would reshuffle every policy and the two effects could not be separated.
	rPlacement := rand.New(rand.NewPCG(seed, 0x6e70677261706801))
	rPolicy := rand.New(rand.NewPCG(seed, 0x6e70677261706802))

	g := &Graph{}
	buildPods(g, p, rPlacement)
	if err := buildPolicies(g, p, rPolicy); err != nil {
		return nil, err
	}

	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("generated graph is inconsistent: %w", err)
	}
	return g, nil
}

// The generated names are positional and nothing but: a graph is identified by
// its params and seed, so a name only has to be stable and unique.
func podName(i int) string    { return fmt.Sprintf("pod-%d", i) }
func nsName(i int) string     { return fmt.Sprintf("ns-%d", i) }
func nodeName(i int) string   { return fmt.Sprintf("node-%d", i) }
func policyName(i int) string { return fmt.Sprintf("np-%d", i) }

// buildPods creates the pods and places them, filling one namespace and one
// node at a time from a size drawn per namespace and per node.
func buildPods(g *Graph, p *Params, r *rand.Rand) {
	g.Pods = make([]*Pod, p.Pods)
	for i := range g.Pods {
		g.Pods[i] = &Pod{Name: podName(i)}
	}

	// Namespaces take consecutive pods, so a namespace is a contiguous run of
	// pod indices.
	for i := 0; i < len(g.Pods); {
		ns := &Namespace{Name: nsName(len(g.Namespaces))}
		g.Namespaces = append(g.Namespaces, ns)
		size := p.PodsPerNamespace.SampleIntRange(r, 1, len(g.Pods))
		for j := 0; j < size && i < len(g.Pods); j++ {
			g.Pods[i].Namespace = ns.Name
			i++
		}
	}

	// Nodes take a shuffled order instead. A node holding a contiguous run
	// would make node membership and namespace membership the same partition,
	// and "the pods of this policy are all on one node" is exactly the
	// coincidence a scale test must not be built on.
	perm := r.Perm(len(g.Pods))
	for i := 0; i < len(perm); {
		node := &Node{Name: nodeName(len(g.Nodes))}
		g.Nodes = append(g.Nodes, node)
		size := p.PodsPerNode.SampleIntRange(r, 1, len(g.Pods))
		for j := 0; j < size && i < len(perm); j++ {
			g.Pods[perm[i]].Node = node.Name
			i++
		}
	}
}

func buildPolicies(g *Graph, p *Params, r *rand.Rand) error {
	if p.Policies == 0 {
		return nil
	}

	podsByNs := g.PodsByNamespace()
	nsNames := make([]string, len(g.Namespaces))
	for i, ns := range g.Namespaces {
		nsNames[i] = ns.Name
	}

	// The type of each policy is allocated up front so the mix comes out exact
	// rather than binomially close to it, then shuffled so that type does not
	// correlate with policy index -- an experiment that truncates the policy
	// list would otherwise get only sameNS policies.
	counts := allocate(p.Policies, p.Mix.weights())
	types := make([]PolicyType, 0, p.Policies)
	for i, n := range counts {
		for range n {
			types = append(types, policyTypes[i])
		}
	}
	r.Shuffle(len(types), func(i, j int) { types[i], types[j] = types[j], types[i] })

	for i, t := range types {
		pol := &Policy{
			Name: policyName(i),
			Type: t,
		}
		if r.Float64() < p.IngressFraction {
			pol.Direction = DirectionIngress
		} else {
			pol.Direction = DirectionEgress
		}

		// The home namespace is picked by drawing a pod rather than a
		// namespace, which weights it by pod count: a namespace holding a
		// tenth of the cluster carries a tenth of the policies, as it would in
		// a cluster where policies follow workloads.
		home := g.Pods[r.IntN(len(g.Pods))].Namespace
		pol.Namespace = home

		inPods := podsByNs[home]
		inDeg := p.InDegree.SampleIntRange(r, 1, len(inPods))
		pol.In = sampleN(r, inPods, inDeg)

		targets := targetNamespaces(r, p, t, home, nsNames)
		outDeg := p.OutDegree.SampleIntRange(r, 0, len(g.Pods))

		if r.Float64() < p.NamespacePeerFraction {
			// A whole-namespace peer set: the out-degree says nothing here,
			// since the peer is the namespace itself and its membership is
			// whatever the namespace holds.
			pol.OutNamespaces = targets
		} else {
			pol.Out = samplePodsAcross(r, podsByNs, targets, outDeg)
		}

		g.Policies = append(g.Policies, pol)
	}

	return nil
}

// targetNamespaces picks the namespaces a policy of type t reaches into. The
// spans are clamped to the namespaces that exist: a CrossNSBroad policy on a
// cluster of six namespaces cannot span eleven, and the honest answer is all
// six rather than an error, since the cluster size is the parameter the user
// varied.
func targetNamespaces(r *rand.Rand, p *Params, t PolicyType, home string, nsNames []string) []string {
	switch t {
	case PolicySameNS:
		return []string{home}
	case PolicyGlobal:
		// Global reaches every namespace, so it is the one type whose targets
		// are not sampled. The order still matters to the peers that come out
		// of it, and nsNames is already in generation order.
		return append([]string(nil), nsNames...)
	case PolicyCrossNSFocused:
		k := p.FocusedNamespaces.SampleIntRange(r, 1, min(FocusedNamespaceLimit, len(nsNames)))
		return sampleN(r, nsNames, k)
	case PolicyCrossNSBroad:
		k := p.BroadNamespaces.SampleIntRange(r, min(FocusedNamespaceLimit+1, len(nsNames)), len(nsNames))
		return sampleN(r, nsNames, k)
	default:
		return []string{home}
	}
}

// samplePodsAcross draws n pods spread over the target namespaces, round-robin
// so that every target is reached before any is reached twice. Spreading
// matters more than the exact count: the type of a policy is a claim about how
// many namespaces its Out set touches, and drawing n pods independently would
// leave a 12-namespace CrossNSBroad policy with peers in four of them.
//
// The result is deduplicated, so a policy asking for more pods than its targets
// hold gets all of them and no more.
func samplePodsAcross(r *rand.Rand, podsByNs map[string][]string, targets []string, n int) []string {
	if n <= 0 || len(targets) == 0 {
		return nil
	}

	// One shuffled cursor per namespace: taking the next pod from a per-target
	// permutation gives distinct pods without the rejection loop a repeated
	// random draw would need, which matters when a target is nearly exhausted.
	perms := make(map[string][]int, len(targets))
	next := make(map[string]int, len(targets))
	for _, ns := range targets {
		perms[ns] = r.Perm(len(podsByNs[ns]))
	}

	out := make([]string, 0, n)
	for len(out) < n {
		progressed := false
		for _, ns := range targets {
			if len(out) == n {
				break
			}
			i := next[ns]
			if i >= len(perms[ns]) {
				continue
			}
			next[ns] = i + 1
			out = append(out, podsByNs[ns][perms[ns][i]])
			progressed = true
		}
		// Every target is exhausted; the graph cannot supply the out-degree
		// asked for.
		if !progressed {
			break
		}
	}
	return out
}

// sampleN draws n distinct elements of from, in an order that is itself random.
// n above len(from) yields the whole set.
func sampleN(r *rand.Rand, from []string, n int) []string {
	if n <= 0 || len(from) == 0 {
		return nil
	}
	if n > len(from) {
		n = len(from)
	}
	perm := r.Perm(len(from))
	out := make([]string, n)
	for i := range n {
		out[i] = from[perm[i]]
	}
	return out
}
