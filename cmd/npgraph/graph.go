package main

import (
	"fmt"
	"slices"
)

// Kind is the type of a vertex in the resource graph. The four kinds are the
// four resources policy resolution depends on: a Pod is placed on a Node and in
// a Namespace, and a NetworkPolicy relates Pods to Pods.
type Kind string

const (
	KindNode      Kind = "node"
	KindNamespace Kind = "namespace"
	KindPod       Kind = "pod"
	KindPolicy    Kind = "netpol"
)

// Direction is which half of a NetworkPolicy an edge set describes. A policy
// vertex carries exactly one: a real NetworkPolicy declaring both policyTypes
// is two vertices, because the In and Out sets of the two directions are
// independent and a single vertex could not say which peer belonged to which.
type Direction string

const (
	DirectionIngress Direction = "ingress"
	DirectionEgress  Direction = "egress"
)

// PolicyType classifies a policy by how far its Out set reaches across
// namespaces. It is a generation parameter rather than something inherent to a
// NetworkPolicy -- the shape of the Out set is what matters to a renderer, and
// the type is the knob that produces a given shape.
type PolicyType string

const (
	PolicySameNS         PolicyType = "sameNS"
	PolicyCrossNSFocused PolicyType = "crossNSFocused"
	PolicyCrossNSBroad   PolicyType = "crossNSBroad"
	PolicyGlobal         PolicyType = "global"
)

// FocusedNamespaceLimit is the largest number of namespaces a CrossNSFocused
// policy's Out set may span; a CrossNSBroad policy spans strictly more. The two
// types differ only by this boundary, so it lives in one place rather than
// being repeated by the generator and the validator.
const FocusedNamespaceLimit = 10

// Node is a cluster node. It holds nothing but its name: nothing about policy
// resolution depends on a node's attributes, only on which pods share one.
type Node struct {
	Name string
}

// Namespace is a cluster namespace.
type Namespace struct {
	Name string
}

// Pod is a pod, placed in exactly one Namespace and on exactly one Node.
type Pod struct {
	Name      string
	Namespace string
	Node      string
}

// Policy is one direction of one NetworkPolicy.
//
// In is the set of pods the policy applies to -- what a NetworkPolicy's
// podSelector picks out -- and so is entirely within Namespace. Out is the peer
// set, which is not: Out names individual pods anywhere in the cluster, and
// OutNamespaces names namespaces every pod of which is a peer, the shape a peer
// with a namespaceSelector and no podSelector takes.
type Policy struct {
	Name      string
	Namespace string
	Direction Direction
	Type      PolicyType

	In            []string
	Out           []string
	OutNamespaces []string
}

// Graph is the resource relationship graph: the vertices of the four kinds and,
// implicitly, the edges between them. Placement edges live on the Pod that has
// exactly one of each, and policy edges live on the Policy, since an edge means
// nothing without knowing which policy and which side of it produced it.
type Graph struct {
	Nodes      []*Node
	Namespaces []*Namespace
	Pods       []*Pod
	Policies   []*Policy
}

// index is the by-name lookup over a graph. It is built on demand rather than
// maintained alongside the slices: the graph is written once and then read, and
// a stale index is a harder bug than a rebuilt one is a cost.
type index struct {
	nodes      map[string]*Node
	namespaces map[string]*Namespace
	pods       map[string]*Pod
	policies   map[string]*Policy
}

func (g *Graph) index() *index {
	idx := &index{
		nodes:      make(map[string]*Node, len(g.Nodes)),
		namespaces: make(map[string]*Namespace, len(g.Namespaces)),
		pods:       make(map[string]*Pod, len(g.Pods)),
		policies:   make(map[string]*Policy, len(g.Policies)),
	}
	for _, n := range g.Nodes {
		idx.nodes[n.Name] = n
	}
	for _, ns := range g.Namespaces {
		idx.namespaces[ns.Name] = ns
	}
	for _, p := range g.Pods {
		idx.pods[p.Name] = p
	}
	for _, p := range g.Policies {
		idx.policies[p.Name] = p
	}
	return idx
}

// PodsByNamespace groups the pod names of the graph by namespace, sorted within
// each namespace so that anything derived from the grouping -- peer lists, the
// YAML -- comes out the same for the same graph.
func (g *Graph) PodsByNamespace() map[string][]string {
	byNs := map[string][]string{}
	for _, p := range g.Pods {
		byNs[p.Namespace] = append(byNs[p.Namespace], p.Name)
	}
	for _, names := range byNs {
		slices.Sort(names)
	}
	return byNs
}

// Validate reports the first way in which g is not a graph a renderer could
// consume: a dangling reference, a duplicated name, or an In set straddling
// namespaces.
//
// Names are unique across kinds and not just within one, because the DOT
// encoding has a single namespace of vertex ids and a pod sharing a name with a
// node would silently become one vertex on the way back in.
func (g *Graph) Validate() error {
	seen := map[string]Kind{}
	claim := func(name string, kind Kind) error {
		if name == "" {
			return fmt.Errorf("%s with an empty name", kind)
		}
		if other, ok := seen[name]; ok {
			return fmt.Errorf("duplicate vertex %q: declared as both %s and %s", name, other, kind)
		}
		seen[name] = kind
		return nil
	}

	for _, n := range g.Nodes {
		if err := claim(n.Name, KindNode); err != nil {
			return err
		}
	}
	for _, ns := range g.Namespaces {
		if err := claim(ns.Name, KindNamespace); err != nil {
			return err
		}
	}
	for _, p := range g.Pods {
		if err := claim(p.Name, KindPod); err != nil {
			return err
		}
	}
	for _, p := range g.Policies {
		if err := claim(p.Name, KindPolicy); err != nil {
			return err
		}
	}

	idx := g.index()
	for _, p := range g.Pods {
		if _, ok := idx.namespaces[p.Namespace]; !ok {
			return fmt.Errorf("pod %q is in unknown namespace %q", p.Name, p.Namespace)
		}
		if _, ok := idx.nodes[p.Node]; !ok {
			return fmt.Errorf("pod %q is on unknown node %q", p.Name, p.Node)
		}
	}

	for _, p := range g.Policies {
		switch p.Direction {
		case DirectionIngress, DirectionEgress:
		default:
			return fmt.Errorf("policy %q has invalid direction %q", p.Name, p.Direction)
		}
		switch p.Type {
		case PolicySameNS, PolicyCrossNSFocused, PolicyCrossNSBroad, PolicyGlobal:
		default:
			return fmt.Errorf("policy %q has invalid type %q", p.Name, p.Type)
		}
		if _, ok := idx.namespaces[p.Namespace]; !ok {
			return fmt.Errorf("policy %q is in unknown namespace %q", p.Name, p.Namespace)
		}
		for _, name := range p.In {
			pod, ok := idx.pods[name]
			if !ok {
				return fmt.Errorf("policy %q selects unknown pod %q", p.Name, name)
			}
			// A NetworkPolicy's podSelector only ever reaches pods in the
			// policy's own namespace, so an In set that leaves it does not
			// describe anything expressible as a NetworkPolicy.
			if pod.Namespace != p.Namespace {
				return fmt.Errorf("policy %q in namespace %q selects pod %q in namespace %q", p.Name, p.Namespace, name, pod.Namespace)
			}
		}
		for _, name := range p.Out {
			if _, ok := idx.pods[name]; !ok {
				return fmt.Errorf("policy %q targets unknown pod %q", p.Name, name)
			}
		}
		for _, name := range p.OutNamespaces {
			if _, ok := idx.namespaces[name]; !ok {
				return fmt.Errorf("policy %q targets unknown namespace %q", p.Name, name)
			}
		}
	}

	return nil
}

// OutNamespaces is the set of namespaces a policy's Out set reaches, counting
// both the pod-level and the namespace-level peers. It is what PolicyType is a
// statement about, so the generator, the classifier and the tests agree on one
// definition of it.
func (g *Graph) OutNamespaces(p *Policy) map[string]bool {
	idx := g.index()
	spanned := map[string]bool{}
	for _, name := range p.Out {
		if pod, ok := idx.pods[name]; ok {
			spanned[pod.Namespace] = true
		}
	}
	for _, name := range p.OutNamespaces {
		spanned[name] = true
	}
	return spanned
}

// OutNamespaceSpan is how many namespaces the Out set reaches.
func (g *Graph) OutNamespaceSpan(p *Policy) int {
	return len(g.OutNamespaces(p))
}
