package policy

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/bowei/xnp/internal/compact"
	"github.com/bowei/xnp/internal/tuple"
)

func renderCompact(t *testing.T, in *Input) *compact.Output {
	t.Helper()
	out, err := RenderCompact(in)
	if err != nil {
		t.Fatalf("RenderCompact: %v", err)
	}
	return out
}

// expandCompact flattens every NodeChunk's PodEntries back into concrete
// Tuples via their Template, the same expansion a node agent would perform
// after fetching a chunk and resolving it against the template dictionary.
func expandCompact(t *testing.T, out *compact.Output) []tuple.Tuple {
	t.Helper()
	var got []tuple.Tuple
	for _, chunk := range out.Nodes {
		for _, pe := range chunk.Pods {
			tmpl, ok := out.Templates[pe.Template]
			if !ok {
				t.Fatalf("node %q pod %s references unknown template %x", chunk.Node, pe.Pod, pe.Template)
			}
			got = append(got, tmpl.Expand(pe.Pod, pe.IPs)...)
		}
	}
	return got
}

// TestRenderCompact_MatchesIndexed is a differential test: expanding every
// compact.Output PodEntry against its Template must reproduce exactly the
// tuple set RenderIndexed produces directly.
func TestRenderCompact_MatchesIndexed(t *testing.T) {
	policies, pods, namespaces := buildRandomFixture(1)
	in := mustInput(t, policies, pods, namespaces)

	want := renderIndexed(t, in)
	got := renderCompact(t, in)
	assertTuples(t, expandCompact(t, got), want.List())
}

func TestRenderCompact_MatchExpressions(t *testing.T) {
	policies, pods, namespaces := buildExpressionFixture()
	in := mustInput(t, policies, pods, namespaces)

	want := renderIndexed(t, in)
	got := renderCompact(t, in)
	assertTuples(t, expandCompact(t, got), want.List())
}

func TestRenderCompact_Empty(t *testing.T) {
	in := mustInput(t, nil, nil, nil)
	out := renderCompact(t, in)
	if len(out.Nodes) != 0 {
		t.Fatalf("expected no node chunks, got %+v", out.Nodes)
	}
	if len(out.Templates) != 0 {
		t.Fatalf("expected no templates, got %+v", out.Templates)
	}
}

// TestRenderCompact_ReplicatedLabelSets_Dedup reuses
// TestRenderIndexed_ReplicatedLabelSets' scenario (many pods, few distinct
// label sets) to check both correctness and the actual point of this
// package: the template dictionary should stay tiny (one entry per distinct
// resolved rule chain) no matter how many pods share it.
func TestRenderCompact_ReplicatedLabelSets_Dedup(t *testing.T) {
	var pods []*corev1.Pod
	var namespaces []*corev1.Namespace
	for n := range 4 {
		nsName := fmt.Sprintf("ns-%d", n)
		namespaces = append(namespaces, namespace(nsName, map[string]string{"team": fmt.Sprintf("team-%d", n%2)}))
		for _, app := range []string{"web", "worker"} {
			for r := range 25 {
				pods = append(pods, pod(nsName, fmt.Sprintf("%s-%d", app, r), fmt.Sprintf("10.%d.0.%d", n, r), map[string]string{"app": app}))
			}
		}
	}
	// 4 namespaces * 25 pods/app * 2 apps = 200 pods total.

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-0", Name: "allow-web-from-team-1"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}},
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "team-1"}},
				}},
			}},
		},
	}
	policies := []*networkingv1.NetworkPolicy{np}
	in := mustInput(t, policies, pods, namespaces)

	want := renderIndexed(t, in)
	got := renderCompact(t, in)
	assertTuples(t, expandCompact(t, got), want.List())

	// Only ns-0's policy selects "web" pods for a non-default ingress
	// template; every other pod (workers, and web pods outside ns-0) is
	// unisolated-ingress and isolated-egress-by-default, all resolving to
	// the very same egress-wildcard-allow / ingress-wildcard-allow
	// template. So despite 200 pods across 4 namespaces, the dictionary
	// should hold only a handful of distinct templates, not one per pod
	// or even one per (namespace, identity) group.
	if got, want := len(got.Templates), 10; got > want {
		t.Errorf("template dictionary has %d entries for 200 pods sharing a handful of distinct rule chains; want <= %d", got, want)
	}
}

// TestRenderCompact_ChunksByNode checks pods are grouped into NodeChunks by
// their scheduled node, the unit a node agent fetches.
func TestRenderCompact_ChunksByNode(t *testing.T) {
	mkPod := func(name, node, ip string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, Labels: map[string]string{"app": "web"}},
			Spec:       corev1.PodSpec{NodeName: node},
			Status:     corev1.PodStatus{PodIP: ip},
		}
	}
	pods := []*corev1.Pod{
		mkPod("a", "node-1", "10.0.0.1"),
		mkPod("b", "node-1", "10.0.0.2"),
		mkPod("c", "node-2", "10.0.0.3"),
	}
	in := mustInput(t, nil, pods, nil)

	out := renderCompact(t, in)

	byNode := map[string]int{}
	for _, chunk := range out.Nodes {
		byNode[chunk.Node] = len(chunk.Pods)
	}
	want := map[string]int{"node-1": 2, "node-2": 1}
	if len(byNode) != len(want) || byNode["node-1"] != want["node-1"] || byNode["node-2"] != want["node-2"] {
		t.Fatalf("node chunk pod counts = %v, want %v", byNode, want)
	}
}

func TestRenderCompact_NamedPortUnsupported(t *testing.T) {
	pods := []*corev1.Pod{pod("ns", "web", "10.0.0.1", map[string]string{"app": "web"})}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "named-port"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"}}},
			}},
		},
	}

	if _, err := NewInput([]*networkingv1.NetworkPolicy{np}, pods, nil); err == nil {
		t.Fatal("NewInput: expected error for named port, got nil")
	}
}
