package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Cluster is the raw material a graph is built from: the three kinds whose
// relationships the graph encodes. Nodes are not listed separately -- a pod
// names its node, and a node with no pods contributes no edges.
type Cluster struct {
	Namespaces []corev1.Namespace
	Pods       []corev1.Pod
	Policies   []netv1.NetworkPolicy
}

// lister reads one kind of resource out of a cluster as JSON. It is an
// injection point: the real one shells out to kubectl, and the tests hand over
// fixtures, which is what keeps the graph-building logic -- the part with the
// selectors in it -- testable without a cluster or a client-go dependency.
type lister func(kind string) ([]byte, error)

// kubectlLister reads resources with kubectl rather than with a client-go
// client. The tool is a generator for experiments, and the cost of a client-go
// dependency and a kubeconfig loader is out of proportion to three list calls
// -- kubectl is already on the machine of anyone who has a -context to pass.
func kubectlLister(context string) lister {
	return func(kind string) ([]byte, error) {
		args := []string{"get", kind, "--all-namespaces", "-o", "json"}
		if context != "" {
			args = append([]string{"--context", context}, args...)
		}
		cmd := exec.Command("kubectl", args...)
		out, err := cmd.Output()
		if err != nil {
			// kubectl says what went wrong on stderr and nothing useful in its
			// exit status, so the message is worth more than the error.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
					return nil, fmt.Errorf("kubectl get %s: %w: %s", kind, err, stderr)
				}
			}
			return nil, fmt.Errorf("kubectl get %s: %w", kind, err)
		}
		return out, nil
	}
}

// FetchCluster lists the resources a graph is built from.
func FetchCluster(list lister) (*Cluster, error) {
	var c Cluster

	nsData, err := list("namespaces")
	if err != nil {
		return nil, err
	}
	var nsList corev1.NamespaceList
	if err := json.Unmarshal(nsData, &nsList); err != nil {
		return nil, fmt.Errorf("decoding namespaces: %w", err)
	}
	c.Namespaces = nsList.Items

	podData, err := list("pods")
	if err != nil {
		return nil, err
	}
	var podList corev1.PodList
	if err := json.Unmarshal(podData, &podList); err != nil {
		return nil, fmt.Errorf("decoding pods: %w", err)
	}
	c.Pods = podList.Items

	npData, err := list("networkpolicies")
	if err != nil {
		return nil, err
	}
	var npList netv1.NetworkPolicyList
	if err := json.Unmarshal(npData, &npList); err != nil {
		return nil, fmt.Errorf("decoding networkpolicies: %w", err)
	}
	c.Policies = npList.Items

	return &c, nil
}

// GraphFromCluster resolves a cluster's selectors into the graph's edges: which
// pods a policy applies to, and which pods and namespaces it names as peers.
//
// Resolving here rather than storing the selectors is the whole point of the
// fetch: the graph is a statement about relationships that already hold, so a
// collected graph and a generated one describe the same kind of object and can
// be fed to the same renderer.
//
// Vertex names are qualified -- "ns/name" for a pod, "ns/name#ingress" for a
// policy -- because the graph has one flat namespace of vertex ids while a
// cluster has one per namespace and kind. A policy declaring both policyTypes
// becomes two vertices, since a Policy vertex carries one direction.
func GraphFromCluster(c *Cluster) (*Graph, error) {
	g := &Graph{}

	nsLabels := make(map[string]labels.Set, len(c.Namespaces))
	for _, ns := range c.Namespaces {
		g.Namespaces = append(g.Namespaces, &Namespace{Name: ns.Name})
		nsLabels[ns.Name] = labels.Set(ns.Labels)
	}

	nodes := map[string]bool{}
	podsByNs := map[string][]*corev1.Pod{}
	for i := range c.Pods {
		pod := &c.Pods[i]
		if _, ok := nsLabels[pod.Namespace]; !ok {
			// A pod whose namespace was not listed cannot be placed, and
			// inventing the namespace would invent whatever labels a
			// namespaceSelector then matched against. This is a race with a
			// namespace being created between two list calls, not a broken
			// cluster, so it is dropped rather than fatal.
			continue
		}
		// A pod that has not been scheduled has no node to attach to. It is
		// still a policy subject, so it gets a placeholder node rather than
		// being dropped: dropping it would understate the policy's In set.
		node := pod.Spec.NodeName
		if node == "" {
			node = unscheduledNode
		}
		nodes[node] = true
		g.Pods = append(g.Pods, &Pod{
			Name:      qualify(pod.Namespace, pod.Name),
			Namespace: pod.Namespace,
			Node:      node,
		})
		podsByNs[pod.Namespace] = append(podsByNs[pod.Namespace], pod)
	}

	for name := range nodes {
		g.Nodes = append(g.Nodes, &Node{Name: name})
	}

	for i := range c.Policies {
		np := &c.Policies[i]
		if _, ok := nsLabels[np.Namespace]; !ok {
			continue
		}
		for _, dir := range directionsOf(np) {
			pol, err := policyFromNetworkPolicy(np, dir, nsLabels, podsByNs)
			if err != nil {
				return nil, err
			}
			g.Policies = append(g.Policies, pol)
		}
	}

	// The type is a statement about the reach of the Out set, which only the
	// assembled graph knows: it needs every namespace of the cluster to say
	// whether a policy spans all of them.
	for _, pol := range g.Policies {
		pol.Type = classifyPolicy(g, pol)
	}

	// Maps were walked to build the nodes and the peers, so the order has to be
	// imposed before the graph is written out; two fetches of an unchanged
	// cluster should differ in nothing.
	sortGraph(g)

	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g, nil
}

// unscheduledNode stands in for the node of a pod that has not been scheduled.
// It is not a valid node name, which is deliberate: it cannot collide with a
// real one, and it is visible as an oddity in the rendered graph.
const unscheduledNode = "<unscheduled>"

func qualify(ns, name string) string { return ns + "/" + name }

// directionsOf reports which directions a NetworkPolicy declares, following the
// API's rule that an omitted policyTypes means ingress, plus egress if any
// egress rule is present.
func directionsOf(np *netv1.NetworkPolicy) []Direction {
	if len(np.Spec.PolicyTypes) == 0 {
		if len(np.Spec.Egress) > 0 {
			return []Direction{DirectionIngress, DirectionEgress}
		}
		return []Direction{DirectionIngress}
	}
	var dirs []Direction
	for _, t := range np.Spec.PolicyTypes {
		switch t {
		case netv1.PolicyTypeIngress:
			dirs = append(dirs, DirectionIngress)
		case netv1.PolicyTypeEgress:
			dirs = append(dirs, DirectionEgress)
		}
	}
	return dirs
}

func policyFromNetworkPolicy(np *netv1.NetworkPolicy, dir Direction, nsLabels map[string]labels.Set, podsByNs map[string][]*corev1.Pod) (*Policy, error) {
	pol := &Policy{
		Name:      qualify(np.Namespace, np.Name) + "#" + string(dir),
		Namespace: np.Namespace,
		Direction: dir,
	}

	sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
	if err != nil {
		return nil, fmt.Errorf("policy %s/%s: podSelector: %w", np.Namespace, np.Name, err)
	}
	for _, pod := range podsByNs[np.Namespace] {
		if sel.Matches(labels.Set(pod.Labels)) {
			pol.In = append(pol.In, qualify(pod.Namespace, pod.Name))
		}
	}

	var peers []netv1.NetworkPolicyPeer
	if dir == DirectionIngress {
		for _, rule := range np.Spec.Ingress {
			peers = append(peers, rule.From...)
		}
	} else {
		for _, rule := range np.Spec.Egress {
			peers = append(peers, rule.To...)
		}
	}

	outPods := map[string]bool{}
	outNs := map[string]bool{}
	for _, peer := range peers {
		if peer.IPBlock != nil {
			// An IPBlock peer names addresses, not pods, and the graph has no
			// vertex for an address. Skipping it loses information; there is no
			// vertex kind to put it in without changing what the graph is.
			continue
		}

		// An omitted namespaceSelector restricts the peer to the policy's own
		// namespace, while an empty one selects every namespace -- the
		// distinction repr.selectorToPred deliberately preserves, and the one
		// place a fetch is most likely to be silently wrong.
		var targetNs []string
		if peer.NamespaceSelector == nil {
			targetNs = []string{np.Namespace}
		} else {
			nsSel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
			if err != nil {
				return nil, fmt.Errorf("policy %s/%s: namespaceSelector: %w", np.Namespace, np.Name, err)
			}
			for ns, l := range nsLabels {
				if nsSel.Matches(l) {
					targetNs = append(targetNs, ns)
				}
			}
		}

		if peer.PodSelector == nil {
			// Every pod of the selected namespaces, which is exactly what a
			// namespace-level Out edge means.
			for _, ns := range targetNs {
				outNs[ns] = true
			}
			continue
		}
		podSel, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("policy %s/%s: peer podSelector: %w", np.Namespace, np.Name, err)
		}
		for _, ns := range targetNs {
			for _, pod := range podsByNs[ns] {
				if podSel.Matches(labels.Set(pod.Labels)) {
					outPods[qualify(pod.Namespace, pod.Name)] = true
				}
			}
		}
	}

	for name := range outPods {
		pol.Out = append(pol.Out, name)
	}
	for name := range outNs {
		pol.OutNamespaces = append(pol.OutNamespaces, name)
	}

	return pol, nil
}
