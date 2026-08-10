package main

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

func TestBuildObjectsCounts(t *testing.T) {
	g := testGraph()
	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}

	if got, want := len(objs.Nodes), len(g.Nodes); got != want {
		t.Errorf("len(Nodes) = %v, want %v", got, want)
	}
	if got, want := len(objs.Namespaces), len(g.Namespaces); got != want {
		t.Errorf("len(Namespaces) = %v, want %v", got, want)
	}
	if got, want := len(objs.Pods), len(g.Pods); got != want {
		t.Errorf("len(Pods) = %v, want %v", got, want)
	}
	if got, want := len(objs.Policies), len(g.Policies); got != want {
		t.Errorf("len(Policies) = %v, want %v", got, want)
	}

	// TypeMeta is set by hand because nothing in this path goes through a
	// scheme, and YAML without an apiVersion is not a manifest.
	if got, want := objs.Pods[0].APIVersion, "v1"; got != want {
		t.Errorf("Pod.APIVersion = %q, want %q", got, want)
	}
	if got, want := objs.Policies[0].Kind, "NetworkPolicy"; got != want {
		t.Errorf("NetworkPolicy.Kind = %q, want %q", got, want)
	}
	if got, want := objs.Namespaces[0].Labels[NamespaceNameLabel], "ns-0"; got != want {
		t.Errorf("Namespace label %q = %q, want %q", NamespaceNameLabel, got, want)
	}

	pod := objs.Pods[0]
	if got, want := pod.Spec.NodeName, "node-0"; got != want {
		t.Errorf("Pod.Spec.NodeName = %q, want %q", got, want)
	}
	if got, want := len(pod.Spec.Containers), 1; got != want {
		t.Fatalf("len(Pod.Spec.Containers) = %v, want %v", got, want)
	}
	if got, want := pod.Spec.Containers[0].Ports[0].ContainerPort, int32(PodPort); got != want {
		t.Errorf("containerPort = %v, want %v", got, want)
	}
}

// The labels are the whole encoding: what the graph says about which pods a
// policy relates has to be exactly what the selectors then pick out of the
// generated objects. This resolves the rendered policies back into pod sets and
// compares them against the graph they came from.
func TestBuildObjectsSelectorsMatchTheGraph(t *testing.T) {
	p := smallParams()
	p.Pods = 120
	p.PodsPerNamespace = Constant(6) // 20 namespaces
	p.Policies = 40
	g, err := Generate(p, 21)
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}

	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}

	for i, pol := range g.Policies {
		np := objs.Policies[i]
		if got, want := np.Namespace, pol.Namespace; got != want {
			t.Fatalf("policy %q rendered into namespace %q, want %q", pol.Name, got, want)
		}

		if got, want := selectedPods(t, objs, np.Namespace, &np.Spec.PodSelector), sorted(pol.In); !slices.Equal(got, want) {
			t.Errorf("policy %q applies to %v, want its In set %v", pol.Name, got, want)
		}

		peers := peersOf(np)
		if got, want := resolvePeers(t, objs, peers), sorted(expectedPeerPods(g, pol)); !slices.Equal(got, want) {
			t.Errorf("policy %q peers resolve to %v, want %v", pol.Name, got, want)
		}
	}
}

// expectedPeerPods is what the policy's Out sets name, expanded: a
// whole-namespace peer stands for every pod in that namespace.
func expectedPeerPods(g *Graph, pol *Policy) []string {
	out := append([]string(nil), pol.Out...)
	byNs := g.PodsByNamespace()
	for _, ns := range pol.OutNamespaces {
		out = append(out, byNs[ns]...)
	}
	return out
}

func peersOf(np *netv1.NetworkPolicy) []netv1.NetworkPolicyPeer {
	var peers []netv1.NetworkPolicyPeer
	for _, rule := range np.Spec.Ingress {
		peers = append(peers, rule.From...)
	}
	for _, rule := range np.Spec.Egress {
		peers = append(peers, rule.To...)
	}
	return peers
}

// resolvePeers evaluates peers against the generated objects the way a policy
// implementation would, and returns the sorted pod names they select.
func resolvePeers(t *testing.T, objs *Objects, peers []netv1.NetworkPolicyPeer) []string {
	t.Helper()

	selected := map[string]bool{}
	for _, peer := range peers {
		if peer.NamespaceSelector == nil {
			t.Fatalf("peer has no namespaceSelector; every generated peer names its namespaces")
		}
		nsSel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			t.Fatalf("namespaceSelector: %v", err)
		}
		podSel := labels.Everything()
		if peer.PodSelector != nil {
			podSel, err = metav1.LabelSelectorAsSelector(peer.PodSelector)
			if err != nil {
				t.Fatalf("podSelector: %v", err)
			}
		}

		inNs := map[string]bool{}
		for _, ns := range objs.Namespaces {
			if nsSel.Matches(labels.Set(ns.Labels)) {
				inNs[ns.Name] = true
			}
		}
		for _, pod := range objs.Pods {
			if inNs[pod.Namespace] && podSel.Matches(labels.Set(pod.Labels)) {
				selected[pod.Name] = true
			}
		}
	}

	return sorted(slices.Collect(maps.Keys(selected)))
}

// selectedPods is the pods of one namespace a selector matches.
func selectedPods(t *testing.T, objs *Objects, ns string, sel *metav1.LabelSelector) []string {
	t.Helper()

	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		t.Fatalf("LabelSelectorAsSelector() = %v, want nil", err)
	}
	var out []string
	for _, pod := range objs.Pods {
		if pod.Namespace == ns && s.Matches(labels.Set(pod.Labels)) {
			out = append(out, pod.Name)
		}
	}
	return sorted(out)
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	slices.Sort(out)
	return out
}

func TestBuildObjectsDirection(t *testing.T) {
	g := testGraph()
	g.Policies[0].Direction = DirectionIngress
	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}
	np := objs.Policies[0]
	if got, want := np.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}; !slices.Equal(got, want) {
		t.Errorf("policyTypes = %v, want %v", got, want)
	}
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Egress) != 0 {
		t.Errorf("ingress rules = %d, egress rules = %d, want 1 and 0", len(np.Spec.Ingress), len(np.Spec.Egress))
	}
	if got, want := np.Spec.Ingress[0].Ports[0].Port.IntValue(), PodPort; got != want {
		t.Errorf("port = %v, want %v", got, want)
	}
	if got, want := *np.Spec.Ingress[0].Ports[0].Protocol, corev1.ProtocolTCP; got != want {
		t.Errorf("protocol = %v, want %v", got, want)
	}

	g.Policies[0].Direction = DirectionEgress
	objs, err = BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}
	np = objs.Policies[0]
	if got, want := np.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}; !slices.Equal(got, want) {
		t.Errorf("policyTypes = %v, want %v", got, want)
	}
	if len(np.Spec.Egress) != 1 || len(np.Spec.Ingress) != 0 {
		t.Errorf("ingress rules = %d, egress rules = %d, want 0 and 1", len(np.Spec.Ingress), len(np.Spec.Egress))
	}
}

// A Global policy's peer is the cluster, not the list of namespaces that
// happen to exist: an empty namespaceSelector is the only thing that says so.
func TestBuildObjectsGlobalPeer(t *testing.T) {
	g := testGraph()
	g.Policies[0].Type = PolicyGlobal
	g.Policies[0].Out = []string{"pod-1", "pod-2"}

	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}
	peers := peersOf(objs.Policies[0])
	if got, want := len(peers), 1; got != want {
		t.Fatalf("global policy has %d peers, want %d", got, want)
	}
	if got := peers[0].NamespaceSelector; got == nil || len(got.MatchLabels) != 0 || len(got.MatchExpressions) != 0 {
		t.Errorf("global peer namespaceSelector = %+v, want an empty selector", got)
	}
	// The pod selector still restricts it to the graph's Out set, so the
	// collapse does not widen what the policy allows.
	if got, want := resolvePeers(t, objs, peers), []string{"pod-1", "pod-2"}; !slices.Equal(got, want) {
		t.Errorf("global peer resolves to %v, want %v", got, want)
	}

	// The same collapse applies to whole-namespace peers covering the cluster.
	g.Policies[0].Out = nil
	g.Policies[0].OutNamespaces = []string{"ns-0", "ns-1"}
	objs, err = BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}
	peers = peersOf(objs.Policies[0])
	if got, want := len(peers), 1; got != want {
		t.Fatalf("global namespace peer count = %d, want %d", got, want)
	}
	if peers[0].PodSelector != nil {
		t.Errorf("whole-namespace peer has podSelector %+v, want none", peers[0].PodSelector)
	}
}

// Pod-level peers are one peer per namespace, because a peer ANDs its
// namespaceSelector with its podSelector.
func TestBuildObjectsPeerPerNamespace(t *testing.T) {
	g := testGraph()
	g.Policies[0].Type = PolicyCrossNSFocused
	g.Policies[0].Out = []string{"pod-1", "pod-2", "pod-3"}

	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}
	peers := peersOf(objs.Policies[0])
	if got, want := len(peers), 2; got != want {
		t.Fatalf("peer count = %d, want %d (one per namespace reached)", got, want)
	}
	if got, want := peers[0].NamespaceSelector.MatchLabels[NamespaceNameLabel], "ns-0"; got != want {
		t.Errorf("first peer namespace = %q, want %q (peers are sorted)", got, want)
	}
	if got, want := resolvePeers(t, objs, peers), []string{"pod-1", "pod-2", "pod-3"}; !slices.Equal(got, want) {
		t.Errorf("peers resolve to %v, want %v", got, want)
	}
}

func TestPodLabels(t *testing.T) {
	g := testGraph()
	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}

	byName := map[string]*corev1.Pod{}
	for _, pod := range objs.Pods {
		byName[pod.Name] = pod
	}

	if got, want := byName["pod-0"].Labels[InLabelPrefix+"np-0"], "true"; got != want {
		t.Errorf("pod-0 in-label = %q, want %q", got, want)
	}
	if got, want := byName["pod-2"].Labels[OutLabelPrefix+"np-0"], "true"; got != want {
		t.Errorf("pod-2 out-label = %q, want %q", got, want)
	}
	// A pod no policy names carries no policy labels at all, which is what
	// makes the selectors mean exactly the graph's edge sets.
	if got := len(byName["pod-1"].Labels); got != 0 {
		t.Errorf("pod-1 labels = %v, want none", byName["pod-1"].Labels)
	}

	// Every label key has to survive API validation, or the manifests are not
	// loadable into anything.
	for _, pod := range objs.Pods {
		for key := range pod.Labels {
			if errs := validation.IsQualifiedName(key); len(errs) > 0 {
				t.Errorf("pod %q label key %q is invalid: %v", pod.Name, key, errs)
			}
		}
	}
}

func TestSanitizeLabelName(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "already valid", in: "np-0", want: "np-0"},
		{name: "qualified fetch name", in: "prod/allow-db#ingress", want: "prod-allow-db-ingress"},
		{name: "leading punctuation", in: "-.-x", want: "x"},
		{name: "empty", in: "", want: "x"},
		{name: "all punctuation", in: "///", want: "x"},
		{name: "too long", in: strings.Repeat("a", 100), want: strings.Repeat("a", 63-len(OutLabelPrefix))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLabelName(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeLabelName(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if errs := validation.IsQualifiedName(OutLabelPrefix + got); len(errs) > 0 {
				t.Errorf("label key %q is invalid: %v", OutLabelPrefix+got, errs)
			}
		})
	}
}

// Two policies whose names sanitize onto the same label would silently merge
// their In sets, which is exactly the wrong answer this tool exists to avoid.
func TestBuildObjectsLabelCollision(t *testing.T) {
	g := testGraph()
	g.Policies[0].Name = "prod/np#ingress"
	g.Policies = append(g.Policies, &Policy{
		Name:      "prod-np-ingress",
		Namespace: "ns-0",
		Direction: DirectionIngress,
		Type:      PolicySameNS,
		In:        []string{"pod-1"},
	})

	_, err := BuildObjects(g)
	if err == nil {
		t.Fatal("BuildObjects() = nil error for colliding policy names, want one")
	}
	if !strings.Contains(err.Error(), "label name") {
		t.Errorf("BuildObjects() = %q, want it to name the collision", err)
	}
}

func TestBuildObjectsRejectsInvalidGraph(t *testing.T) {
	g := testGraph()
	g.Pods[0].Namespace = "nope"
	if _, err := BuildObjects(g); err == nil {
		t.Error("BuildObjects() = nil error for an invalid graph, want one")
	}
}

func TestWriteK8sYAML(t *testing.T) {
	g := testGraph()
	objs, err := BuildObjects(g)
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}

	var buf bytes.Buffer
	if err := WriteK8sYAML(&buf, objs); err != nil {
		t.Fatalf("WriteK8sYAML() = %v, want nil", err)
	}

	docs := splitYAML(buf.String())
	if got, want := len(docs), len(objs.Nodes)+len(objs.Namespaces)+len(objs.Pods)+len(objs.Policies); got != want {
		t.Fatalf("%d documents, want %d", got, want)
	}

	// Applying the stream top to bottom has to work, so a namespace comes
	// before the pods in it and a node before the pods on it.
	var kinds []string
	for _, doc := range docs {
		var meta metav1.TypeMeta
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatalf("unmarshalling document: %v\n%s", err, doc)
		}
		kinds = append(kinds, meta.Kind)
	}
	wantOrder := []string{"Node", "Namespace", "Pod", "NetworkPolicy"}
	lastSeen := 0
	for _, kind := range kinds {
		idx := slices.Index(wantOrder, kind)
		if idx < 0 {
			t.Fatalf("unexpected kind %q", kind)
		}
		if idx < lastSeen {
			t.Errorf("kind %q comes after %q; want the stream in dependency order %v", kind, wantOrder[lastSeen], wantOrder)
		}
		lastSeen = idx
	}

	// The YAML has to deserialize back into the API types it was built from --
	// that is the only claim a manifest makes.
	var np netv1.NetworkPolicy
	if err := yaml.Unmarshal([]byte(docs[len(docs)-1]), &np); err != nil {
		t.Fatalf("unmarshalling the policy: %v", err)
	}
	if got, want := np.Name, "np-0"; got != want {
		t.Errorf("NetworkPolicy name = %q, want %q", got, want)
	}
	if got, want := np.Spec.PodSelector.MatchLabels[InLabelPrefix+"np-0"], "true"; got != want {
		t.Errorf("podSelector matchLabels = %v, want the in-label", np.Spec.PodSelector.MatchLabels)
	}
}

func splitYAML(s string) []string {
	var docs []string
	for _, doc := range strings.Split(s, "---\n") {
		if strings.TrimSpace(doc) != "" {
			docs = append(docs, doc)
		}
	}
	return docs
}
