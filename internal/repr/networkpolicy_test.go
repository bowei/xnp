package repr

import (
	"net/netip"
	"testing"

	"github.com/bowei/xnp/internal/tuple"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

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
					{Protocol: &protoUDP, Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 53}, EndPort: new(int32(54))},
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
	if rule.Ports[0].Protocol != tuple.ProtocolTCP || *rule.Ports[0].Port != 80 {
		t.Errorf("Ports[0] = %+v, want TCP/80 (protocol defaults to TCP when unset)", rule.Ports[0])
	}
	if rule.Ports[1].Protocol != tuple.ProtocolUDP || *rule.Ports[1].Port != 53 || *rule.Ports[1].EndPort != 54 {
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
