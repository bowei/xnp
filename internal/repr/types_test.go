package repr

import (
	"net/netip"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestSymbolTable_InternDedupesAndRoundTrips(t *testing.T) {
	tbl := NewSymbolTable()
	a := tbl.Intern("foo")
	b := tbl.Intern("bar")
	c := tbl.Intern("foo")

	if a != c {
		t.Fatalf("Intern(\"foo\") = %d, then %d; want equal IDs for equal strings", a, c)
	}
	if a == b {
		t.Fatalf("Intern(\"foo\") and Intern(\"bar\") collided: %d", a)
	}
	if got := tbl.String(a); got != "foo" {
		t.Errorf("String(%d) = %q, want \"foo\"", a, got)
	}
	if got := tbl.String(b); got != "bar" {
		t.Errorf("String(%d) = %q, want \"bar\"", b, got)
	}
}

func TestNewLabelSet(t *testing.T) {
	tbl := NewSymbolTable()
	ls := NewLabelSet(map[string]string{"app": "web", "env": "prod"}, tbl)

	if got := ls.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	appKey := tbl.Intern("app")
	v, ok := ls.Get(appKey)
	if !ok {
		t.Fatalf("Get(app) missing")
	}
	if got := tbl.String(v); got != "web" {
		t.Errorf("Get(app) = %q, want \"web\"", got)
	}
	if _, ok := ls.Get(tbl.Intern("missing")); ok {
		t.Errorf("Get(missing) = ok, want !ok")
	}
}

func TestNewLabelSet_SortedOrderIsCanonical(t *testing.T) {
	tbl := NewSymbolTable()
	labels := map[string]string{"z": "1", "a": "2", "m": "3"}

	// map iteration order is randomized by the runtime; building the same
	// LabelSet repeatedly must still produce an identical sorted slice, or
	// downstream hashing/identity collapsing (lookup.go) would silently
	// treat identical label sets as distinct.
	first := NewLabelSet(labels, tbl)
	for i := 0; i < 10; i++ {
		got := NewLabelSet(labels, tbl)
		if len(got.sorted) != len(first.sorted) {
			t.Fatalf("iteration %d: sorted length = %d, want %d", i, len(got.sorted), len(first.sorted))
		}
		for j := range got.sorted {
			if got.sorted[j] != first.sorted[j] {
				t.Fatalf("iteration %d: sorted[%d] = %d, want %d", i, j, got.sorted[j], first.sorted[j])
			}
		}
	}
}

func TestNewPod(t *testing.T) {
	tbl := NewSymbolTable()

	t.Run("single IP", func(t *testing.T) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a", Labels: map[string]string{"app": "web"}},
			Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
		}
		got, err := NewPod(p, tbl)
		if err != nil {
			t.Fatalf("NewPod: %v", err)
		}
		if got.Name != "a" {
			t.Errorf("Name = %q, want \"a\"", got.Name)
		}
		if tbl.String(got.Namespace) != "ns" {
			t.Errorf("Namespace = %q, want \"ns\"", tbl.String(got.Namespace))
		}
		want := netip.MustParseAddr("10.0.0.1")
		if len(got.IPs) != 1 || got.IPs[0] != want {
			t.Errorf("IPs = %v, want [%v]", got.IPs, want)
		}
	})

	t.Run("dual-stack PodIPs takes precedence over PodIP", func(t *testing.T) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"},
			Status: corev1.PodStatus{
				PodIP:  "10.0.0.1",
				PodIPs: []corev1.PodIP{{IP: "10.0.0.1"}, {IP: "fd00::1"}},
			},
		}
		got, err := NewPod(p, tbl)
		if err != nil {
			t.Fatalf("NewPod: %v", err)
		}
		want := []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("fd00::1")}
		if len(got.IPs) != len(want) || got.IPs[0] != want[0] || got.IPs[1] != want[1] {
			t.Errorf("IPs = %v, want %v", got.IPs, want)
		}
	})

	t.Run("no IP assigned yet", func(t *testing.T) {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "c"}}
		got, err := NewPod(p, tbl)
		if err != nil {
			t.Fatalf("NewPod: %v", err)
		}
		if got.IPs != nil {
			t.Errorf("IPs = %v, want nil", got.IPs)
		}
	})

	t.Run("invalid IP errors", func(t *testing.T) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "d"},
			Status:     corev1.PodStatus{PodIP: "not-an-ip"},
		}
		if _, err := NewPod(p, tbl); err == nil {
			t.Fatalf("NewPod: want error for invalid IP, got nil")
		}
	})
}

func TestNewNamespace(t *testing.T) {
	tbl := NewSymbolTable()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{"tier": "prod"}}}

	got := NewNamespace(ns, tbl)
	if tbl.String(got.Name) != "team-a" {
		t.Errorf("Name = %q, want \"team-a\"", tbl.String(got.Name))
	}
	tierKey := tbl.Intern("tier")
	v, ok := got.Labels.Get(tierKey)
	if !ok || tbl.String(v) != "prod" {
		t.Errorf("Labels[tier] = %q (ok=%v), want \"prod\"", tbl.String(v), ok)
	}
}

func int32p(v int32) *int32 { return &v }

func TestNewNetworkPolicy(t *testing.T) {
	tbl := NewSymbolTable()
	protoUDP := corev1.ProtocolUDP

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "np1"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{
					{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}}},
					{NamespaceSelector: &metav1.LabelSelector{}},
					{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/24", Except: []string{"10.0.0.128/25"}}},
				},
				Ports: []networkingv1.NetworkPolicyPort{
					{Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 80}},
					{Protocol: &protoUDP, Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 53}, EndPort: int32p(54)},
				},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}

	got, err := NewNetworkPolicy(np, tbl)
	if err != nil {
		t.Fatalf("NewNetworkPolicy: %v", err)
	}
	if tbl.String(got.Namespace) != "ns" || got.Name != "np1" {
		t.Fatalf("Namespace/Name = %q/%q, want \"ns\"/\"np1\"", tbl.String(got.Namespace), got.Name)
	}
	if len(got.PolicyTypes) != 2 {
		t.Fatalf("PolicyTypes = %v, want 2 entries", got.PolicyTypes)
	}
	if len(got.Ingress) != 1 || len(got.Egress) != 1 {
		t.Fatalf("Ingress/Egress = %d/%d rules, want 1/1", len(got.Ingress), len(got.Egress))
	}

	rule := got.Ingress[0]
	if len(rule.Peers) != 3 {
		t.Fatalf("Peers = %d, want 3", len(rule.Peers))
	}
	if rule.Peers[0].PodSelector == nil || rule.Peers[0].PodSelector.MatchLabels["role"] != "client" {
		t.Errorf("Peers[0].PodSelector = %v, want role=client", rule.Peers[0].PodSelector)
	}
	if rule.Peers[1].NamespaceSelector == nil {
		t.Errorf("Peers[1].NamespaceSelector = nil, want non-nil")
	}
	block := rule.Peers[2].IPBlock
	if block == nil {
		t.Fatalf("Peers[2].IPBlock = nil, want non-nil")
	}
	if block.CIDR != netip.MustParsePrefix("10.0.0.0/24") {
		t.Errorf("IPBlock.CIDR = %v, want 10.0.0.0/24", block.CIDR)
	}
	if len(block.Except) != 1 || block.Except[0] != netip.MustParsePrefix("10.0.0.128/25") {
		t.Errorf("IPBlock.Except = %v, want [10.0.0.128/25]", block.Except)
	}

	if len(rule.Ports) != 2 {
		t.Fatalf("Ports = %d, want 2", len(rule.Ports))
	}
	if rule.Ports[0].Protocol != ProtocolTCP || *rule.Ports[0].Port != 80 {
		t.Errorf("Ports[0] = %+v, want TCP/80 (protocol defaults to TCP when unset)", rule.Ports[0])
	}
	if rule.Ports[1].Protocol != ProtocolUDP || *rule.Ports[1].Port != 53 || *rule.Ports[1].EndPort != 54 {
		t.Errorf("Ports[1] = %+v, want UDP/53-54", rule.Ports[1])
	}
}

func TestNewNetworkPolicy_NamedPortUnsupported(t *testing.T) {
	tbl := NewSymbolTable()
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "np1"},
		Spec: networkingv1.NetworkPolicySpec{
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"}}},
			}},
		},
	}
	if _, err := NewNetworkPolicy(np, tbl); err == nil {
		t.Fatalf("NewNetworkPolicy: want error for named port, got nil")
	}
}

func TestNewNetworkPolicy_InvalidCIDR(t *testing.T) {
	tbl := NewSymbolTable()
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "np1"},
		Spec: networkingv1.NetworkPolicySpec{
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "not-a-cidr"}}},
			}},
		},
	}
	if _, err := NewNetworkPolicy(np, tbl); err == nil {
		t.Fatalf("NewNetworkPolicy: want error for invalid CIDR, got nil")
	}
}
