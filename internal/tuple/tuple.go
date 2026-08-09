// Package tuple defines the compiled filtering-rule type that policy
// renderers produce, as described in the "Network policy computation"
// section of the repo README.
package tuple

import (
	"fmt"
	"net/netip"

	"github.com/bowei/xnp/internal/eqc"
	corev1 "k8s.io/api/core/v1"
)

// Tuple of rendered policy.
type Tuple struct {
	// Source equivalence class. If nil, then source is a SourceCIDR.
	Source     *eqc.Class
	SourceCIDR netip.Prefix

	// Dest equivalence class. If nil, then dest is a DestCIDR.
	Dest     *eqc.Class
	DestCIDR netip.Prefix

	// Port/EndPort/Protocol are all zero-valued to mean "any" together;
	// Protocol is only meaningful when Port is set.
	Port    *int32
	EndPort *int32

	Protocol  Protocol
	Direction Direction
	Verdict   Verdict
}

// String renders the Tuple on one line, for logs and test failure messages.
// The form is
//
//	ingress allow default/eqc:1f0c… -> 10.0.0.0/8 tcp:80-443
//
// where either endpoint is a class, a CIDR, or "any" when neither is set — the
// trailing wildcard of a chain, which carries the default verdict.
func (tup *Tuple) String() string {
	return fmt.Sprintf("%s %s %s -> %s %s",
		tup.Direction, tup.Verdict,
		endpoint(tup.Source, tup.SourceCIDR),
		endpoint(tup.Dest, tup.DestCIDR),
		tup.portString())
}

func endpoint(class *eqc.Class, cidr netip.Prefix) string {
	switch {
	case class != nil:
		// A Class has no name of its own, and its members are unordered, so
		// namespace plus label hash is the only identity that is stable across
		// renders. Collisions are possible but this is a debugging string.
		return fmt.Sprintf("%s/eqc:%016x", class.Namespace(), class.KV().Hash())
	case cidr.IsValid():
		return cidr.String()
	default:
		return "any"
	}
}

// portString renders the port range together with the protocol, since Protocol
// is only meaningful when Port is set: an unset Port is "any" traffic, not "any
// port of tcp".
func (tup *Tuple) portString() string {
	switch {
	case tup.Port == nil:
		return "any"
	case tup.EndPort == nil:
		return fmt.Sprintf("%s:%d", tup.Protocol, *tup.Port)
	default:
		return fmt.Sprintf("%s:%d-%d", tup.Protocol, *tup.Port, *tup.EndPort)
	}
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
