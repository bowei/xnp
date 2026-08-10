package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

const (
	// LabelPrefix namespaces every label npgraph puts on a generated object, so
	// a generated cluster can be told apart from whatever else is in it.
	LabelPrefix = "npgraph.io/"

	// InLabelPrefix and OutLabelPrefix are the per-policy labels the graph's
	// edges become: a pod in a policy's In set carries the policy's in-label
	// and is selected by its podSelector, and a pod in the Out set carries the
	// out-label and is selected by its peers.
	//
	// One label per policy per side is the encoding that makes the K8s objects
	// mean exactly what the graph says. Reusing an existing label (a role, an
	// app name) would make the selectors smaller, but then the selected set
	// would be whatever else happens to carry that label, and the point of
	// generating from a graph is that the answer is known in advance.
	InLabelPrefix  = LabelPrefix + "in-"
	OutLabelPrefix = LabelPrefix + "out-"

	// NamespaceNameLabel is the label kubelet-less clusters and the API server
	// alike put on every namespace, and what a namespaceSelector naming one
	// specific namespace has to match on.
	NamespaceNameLabel = "kubernetes.io/metadata.name"

	// PodPort is the single port every generated pod exposes. The doc allows
	// one port per pod for now, so the port is a constant rather than a
	// parameter.
	PodPort = 8080

	labelTrue = "true"
)

// Objects is the K8s rendering of a graph, kept as typed slices rather than a
// serialized blob so that tests -- and a future loader -- can assert against
// the objects instead of against YAML.
type Objects struct {
	Nodes      []*corev1.Node
	Namespaces []*corev1.Namespace
	Pods       []*corev1.Pod
	Policies   []*netv1.NetworkPolicy
}

// BuildObjects renders g as Kubernetes resources.
func BuildObjects(g *Graph) (*Objects, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}

	objs := &Objects{}

	for _, n := range g.Nodes {
		objs.Nodes = append(objs.Nodes, &corev1.Node{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{Name: n.Name},
		})
	}

	for _, ns := range g.Namespaces {
		objs.Namespaces = append(objs.Namespaces, &corev1.Namespace{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{
				Name: ns.Name,
				// The API server sets this label itself on a real cluster; it
				// is written out because the manifests have to be loadable
				// into something that does not, and because every
				// namespaceSelector below matches on it.
				Labels: map[string]string{NamespaceNameLabel: ns.Name},
			},
		})
	}

	// The per-policy labels are collected across all policies before any pod is
	// built, since a pod is in the In set of one policy and the Out set of
	// another and carries both labels.
	podLabels := make(map[string]map[string]string, len(g.Pods))
	for _, p := range g.Pods {
		podLabels[p.Name] = map[string]string{}
	}
	labelNames, err := policyLabelNames(g)
	if err != nil {
		return nil, err
	}
	for _, pol := range g.Policies {
		names := labelNames[pol.Name]
		for _, pod := range pol.In {
			podLabels[pod][names.in] = labelTrue
		}
		for _, pod := range pol.Out {
			podLabels[pod][names.out] = labelTrue
		}
	}

	for _, p := range g.Pods {
		objs.Pods = append(objs.Pods, &corev1.Pod{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      p.Name,
				Namespace: p.Namespace,
				Labels:    podLabels[p.Name],
			},
			Spec: corev1.PodSpec{
				NodeName: p.Node,
				Containers: []corev1.Container{{
					Name:  "app",
					Image: "registry.k8s.io/pause:3.10",
					Ports: []corev1.ContainerPort{{
						ContainerPort: PodPort,
						Protocol:      corev1.ProtocolTCP,
					}},
				}},
			},
		})
	}

	for _, pol := range g.Policies {
		np, err := buildNetworkPolicy(g, pol, labelNames[pol.Name])
		if err != nil {
			return nil, err
		}
		objs.Policies = append(objs.Policies, np)
	}

	return objs, nil
}

type policyLabels struct{ in, out string }

// policyLabelNames assigns the in- and out-label of every policy, and reports a
// collision rather than letting two policies share a label.
//
// A generated graph names its policies np-0, np-1, ... and could skip all of
// this, but a fetched one names them after real objects: "prod/allow-db#egress"
// is not a label value, and sanitizing it can map two distinct policies onto
// one name. Two policies sharing a label would silently merge their In sets,
// which is the kind of wrong answer this tool exists to avoid.
func policyLabelNames(g *Graph) (map[string]policyLabels, error) {
	names := make(map[string]policyLabels, len(g.Policies))
	owner := map[string]string{}
	for _, pol := range g.Policies {
		sanitized := sanitizeLabelName(pol.Name)
		if prev, ok := owner[sanitized]; ok {
			return nil, fmt.Errorf("policies %q and %q both map onto label name %q; rename one", prev, pol.Name, sanitized)
		}
		owner[sanitized] = pol.Name
		names[pol.Name] = policyLabels{in: InLabelPrefix + sanitized, out: OutLabelPrefix + sanitized}
	}
	return names, nil
}

var labelUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// sanitizeLabelName maps a policy name onto the part of a label key after the
// prefix: alphanumerics, '-', '_' and '.', starting and ending with an
// alphanumeric, and short enough that the prefix plus the name stays inside the
// 63-character limit.
func sanitizeLabelName(name string) string {
	s := labelUnsafe.ReplaceAllString(name, "-")
	s = strings.Trim(s, "-._")
	if s == "" {
		s = "x"
	}
	if maxLen := 63 - len(OutLabelPrefix); len(s) > maxLen {
		s = strings.Trim(s[:maxLen], "-._")
	}
	return s
}

func buildNetworkPolicy(g *Graph, pol *Policy, labels policyLabels) (*netv1.NetworkPolicy, error) {
	np := &netv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      sanitizeLabelName(pol.Name),
			Namespace: pol.Namespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{labels.in: labelTrue}},
		},
	}

	peers := buildPeers(g, pol, labels)
	port := intstr.FromInt32(PodPort)
	proto := corev1.ProtocolTCP
	ports := []netv1.NetworkPolicyPort{{Protocol: &proto, Port: &port}}

	switch pol.Direction {
	case DirectionIngress:
		np.Spec.PolicyTypes = []netv1.PolicyType{netv1.PolicyTypeIngress}
		np.Spec.Ingress = []netv1.NetworkPolicyIngressRule{{From: peers, Ports: ports}}
	case DirectionEgress:
		np.Spec.PolicyTypes = []netv1.PolicyType{netv1.PolicyTypeEgress}
		np.Spec.Egress = []netv1.NetworkPolicyEgressRule{{To: peers, Ports: ports}}
	default:
		return nil, fmt.Errorf("policy %q has invalid direction %q", pol.Name, pol.Direction)
	}

	return np, nil
}

// buildPeers turns a policy's Out sets into NetworkPolicyPeers.
//
// Pod-level peers are grouped by namespace -- one peer per namespace, matching
// that namespace by name and the out-label within it -- because a peer's
// namespaceSelector and podSelector are ANDed, so a single peer naming several
// namespaces would also have to name the pods of all of them together.
//
// The exception is a Global policy, which is collapsed to one peer with an
// empty namespaceSelector. That is not merely shorter: "every namespace" is
// what the type means, and spelling it out as a list of the namespaces that
// exist today would say something weaker.
func buildPeers(g *Graph, pol *Policy, labels policyLabels) []netv1.NetworkPolicyPeer {
	var peers []netv1.NetworkPolicyPeer

	if len(pol.Out) > 0 {
		if pol.Type == PolicyGlobal {
			peers = append(peers, netv1.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{labels.out: labelTrue}},
			})
		} else {
			for _, ns := range namespacesOf(g, pol.Out) {
				peers = append(peers, netv1.NetworkPolicyPeer{
					NamespaceSelector: namespaceByName(ns),
					PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{labels.out: labelTrue}},
				})
			}
		}
	}

	// Whole-namespace peers carry no podSelector at all, which is the API's way
	// of saying "every pod in the selected namespaces" -- and an empty
	// podSelector would say the same thing, but the omission is what a person
	// reading the YAML expects to see.
	if len(pol.OutNamespaces) > 0 {
		if pol.Type == PolicyGlobal && len(pol.OutNamespaces) == len(g.Namespaces) {
			peers = append(peers, netv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}})
		} else {
			for _, ns := range slices.Sorted(slices.Values(pol.OutNamespaces)) {
				peers = append(peers, netv1.NetworkPolicyPeer{NamespaceSelector: namespaceByName(ns)})
			}
		}
	}

	return peers
}

func namespaceByName(ns string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{NamespaceNameLabel: ns}}
}

// namespacesOf is the sorted set of namespaces the named pods live in.
func namespacesOf(g *Graph, pods []string) []string {
	idx := g.index()
	seen := map[string]bool{}
	var out []string
	for _, name := range pods {
		pod, ok := idx.pods[name]
		if !ok || seen[pod.Namespace] {
			continue
		}
		seen[pod.Namespace] = true
		out = append(out, pod.Namespace)
	}
	slices.Sort(out)
	return out
}

// WriteK8sYAML writes the objects as a multi-document YAML stream, in
// dependency order: namespaces exist before the pods in them, and nodes before
// the pods scheduled onto them, so the stream can be applied top to bottom.
func WriteK8sYAML(w io.Writer, objs *Objects) error {
	bw := bufio.NewWriter(w)

	write := func(obj any) error {
		data, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		if _, err := bw.WriteString("---\n"); err != nil {
			return err
		}
		_, err = bw.Write(data)
		return err
	}

	for _, n := range objs.Nodes {
		if err := write(n); err != nil {
			return err
		}
	}
	for _, ns := range objs.Namespaces {
		if err := write(ns); err != nil {
			return err
		}
	}
	for _, p := range objs.Pods {
		if err := write(p); err != nil {
			return err
		}
	}
	for _, np := range objs.Policies {
		if err := write(np); err != nil {
			return err
		}
	}

	return bw.Flush()
}
