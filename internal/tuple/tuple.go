// Package tuple defines the compiled filtering-rule type that policy
// renderers produce, as described in the "Network policy computation"
// section of the repo README.
package tuple

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Tuple is one compiled filtering rule to be enforced at Pod. Rules for a
// given (Pod, Direction) form an ordered, first-match-wins chain: explicit
// "allow" tuples followed by a single trailing wildcard tuple that carries
// the default verdict (allow if the pod is not isolated for that direction,
// deny if it is).
type Tuple struct {
	Pod  types.NamespacedName
	Src  string // concrete IP or CIDR; "" means any (wildcard)
	Dest string // concrete IP or CIDR; "" means any (wildcard)

	// Port/EndPort/Protocol are all zero-valued to mean "any" together;
	// Protocol is only meaningful when Port is set.
	Port    *int32
	EndPort *int32

	Protocol  Protocol
	Direction Direction
	Verdict   Verdict
}

// Protocol is corev1.Protocol packed into a byte instead of a string; the
// NetworkPolicy API only ever produces one of the three values below.
type Protocol uint8

const (
	ProtocolTCP Protocol = iota
	ProtocolUDP
	ProtocolSCTP
)

func (p Protocol) String() string {
	switch p {
	case ProtocolTCP:
		return "tcp"
	case ProtocolUDP:
		return "udp"
	case ProtocolSCTP:
		return "sctp"
	default:
		return "unknown"
	}
}

func ProtocolFromK8s(p corev1.Protocol) (Protocol, error) {
	switch p {
	case corev1.ProtocolTCP:
		return ProtocolTCP, nil
	case corev1.ProtocolUDP:
		return ProtocolUDP, nil
	case corev1.ProtocolSCTP:
		return ProtocolSCTP, nil
	default:
		return 0, fmt.Errorf("unsupported protocol %q", p)
	}
}

// Direction is the traffic direction a Tuple applies to.
type Direction uint8

const (
	DirectionIngress Direction = iota
	DirectionEgress
)

func (d Direction) String() string {
	switch d {
	case DirectionIngress:
		return "ingress"
	case DirectionEgress:
		return "egress"
	default:
		return "unknown"
	}
}

// Verdict is the action a Tuple applies.
type Verdict uint8

const (
	VerdictAllow Verdict = iota
	VerdictDeny
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictDeny:
		return "deny"
	default:
		return "unknown"
	}
}
