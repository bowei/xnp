// Package tuple defines the compiled filtering-rule type that policy
// renderers produce, as described in the "Network policy computation"
// section of the repo README.
package tuple

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

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
	Port     *int32
	EndPort  *int32
	Protocol corev1.Protocol

	Direction Direction
	Verdict   Verdict
}
