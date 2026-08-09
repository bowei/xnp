package npinternal

import (
	"net/netip"
	"testing"

	"github.com/bowei/xnp/internal/repr/convert"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestNewNetworkPolicy(t *testing.T) {
	protoUDP := corev1.ProtocolUDP
	endPort := int32(54)

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
					{Protocol: &protoUDP, Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 53}, EndPort: &endPort},
				},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}

	got, err := NewNetworkPolicy(np)
	if err != nil {
		t.Fatalf("NewNetworkPolicy: %v", err)
	}
	if got.Namespace != "ns" || got.Name != "np1" {
		t.Fatalf("Namespace/Name = %q/%q, want \"ns\"/\"np1\"", got.Namespace, got.Name)
	}
	if len(got.PolicyTypes) != 2 {
		t.Fatalf("PolicyTypes = %v, want 2 entries", got.PolicyTypes)
	}

	web := convert.LabelsToKV(map[string]string{"app": "web"})
	db := convert.LabelsToKV(map[string]string{"app": "db"})
	if !got.PodSelector.Match(&web) || got.PodSelector.Match(&db) {
		t.Errorf("PodSelector matched (web, db) = (%v, %v), want (true, false)",
			got.PodSelector.Match(&web), got.PodSelector.Match(&db))
	}

	if len(got.Ingress) != 1 || len(got.Egress) != 1 {
		t.Fatalf("Ingress/Egress = %d/%d rules, want 1/1", len(got.Ingress), len(got.Egress))
	}

	rule := got.Ingress[0]
	if len(rule.Peers) != 3 {
		t.Fatalf("Peers = %d, want 3", len(rule.Peers))
	}

	client := convert.LabelsToKV(map[string]string{"role": "client"})
	if p := rule.Peers[0]; p.PodSelector == nil || !p.PodSelector.Match(&client) {
		t.Errorf("Peers[0].PodSelector did not match role=client")
	} else if p.NamespaceSelector != nil {
		t.Errorf("Peers[0].NamespaceSelector = non-nil, want nil for an omitted selector")
	}
	if p := rule.Peers[1]; p.NamespaceSelector == nil {
		t.Errorf("Peers[1].NamespaceSelector = nil, want non-nil")
	} else if !p.NamespaceSelector.Match(&db) {
		t.Errorf("Peers[1].NamespaceSelector is empty, want it to match everything")
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
	if p := rule.Ports[0]; p.Protocol != corev1.ProtocolTCP || p.Port == nil || *p.Port != 80 || p.EndPort != nil {
		t.Errorf("Ports[0] = %+v, want TCP/80 (protocol defaults to TCP when unset)", p)
	}
	if p := rule.Ports[1]; p.Protocol != corev1.ProtocolUDP || p.Port == nil || *p.Port != 53 || p.EndPort == nil || *p.EndPort != 54 {
		t.Errorf("Ports[1] = %+v, want UDP/53-54", p)
	}
}

func TestNewNetworkPolicy_EmptySpec(t *testing.T) {
	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "np1"}}
	got, err := NewNetworkPolicy(np)
	if err != nil {
		t.Fatalf("NewNetworkPolicy: %v", err)
	}
	// An empty podSelector selects every pod in the policy's namespace.
	unlabeled := convert.LabelsToKV(nil)
	if got.PodSelector == nil || !got.PodSelector.Match(&unlabeled) {
		t.Errorf("PodSelector did not match an unlabeled pod, want the empty selector to match everything")
	}
	if len(got.Ingress) != 0 || len(got.Egress) != 0 {
		t.Errorf("Ingress/Egress = %d/%d rules, want 0/0", len(got.Ingress), len(got.Egress))
	}
}

func TestNewNetworkPolicy_Errors(t *testing.T) {
	endPort := int32(80)
	lowEndPort := int32(53)
	protoBogus := corev1.Protocol("BOGUS")

	for _, tc := range []struct {
		name string
		spec networkingv1.NetworkPolicySpec
	}{
		{
			name: "named port unsupported",
			spec: networkingv1.NetworkPolicySpec{
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					Ports: []networkingv1.NetworkPolicyPort{{Port: &intstr.IntOrString{Type: intstr.String, StrVal: "http"}}},
				}},
			},
		},
		{
			name: "invalid CIDR",
			spec: networkingv1.NetworkPolicySpec{
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "not-a-cidr"}}},
				}},
			},
		},
		{
			name: "invalid except CIDR",
			spec: networkingv1.NetworkPolicySpec{
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
						CIDR: "10.0.0.0/24", Except: []string{"nope"},
					}}},
				}},
			},
		},
		{
			name: "unsupported selector operator",
			spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "app", Operator: "Bogus",
				}}},
			},
		},
		{
			name: "unsupported selector operator in a peer",
			spec: networkingv1.NetworkPolicySpec{
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key: "tier", Operator: "Bogus",
						}}},
					}},
				}},
			},
		},
		{
			name: "unsupported protocol",
			spec: networkingv1.NetworkPolicySpec{
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protoBogus}},
				}},
			},
		},
		{
			name: "endPort without port",
			spec: networkingv1.NetworkPolicySpec{
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					Ports: []networkingv1.NetworkPolicyPort{{EndPort: &endPort}},
				}},
			},
		},
		{
			name: "endPort below port",
			spec: networkingv1.NetworkPolicySpec{
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					Ports: []networkingv1.NetworkPolicyPort{{
						Port:    &intstr.IntOrString{Type: intstr.Int, IntVal: 80},
						EndPort: &lowEndPort,
					}},
				}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			np := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "np1"},
				Spec:       tc.spec,
			}
			if _, err := NewNetworkPolicy(np); err == nil {
				t.Fatalf("NewNetworkPolicy: want error, got nil")
			}
		})
	}
}
