// Package repr holds memory-efficient mirrors of the Kubernetes types
// internal/policy renders (Pod, NetworkPolicy, Namespace), carrying only the
// fields policy resolution needs, in the form it needs them:
//
//   - IPs as netip.Addr/netip.Prefix instead of strings.
//   - Labels as a kvec kv.KV instead of map[string]string.
//   - Selectors precompiled into kvec pred.Predicates instead of
//     metav1.LabelSelectors, so matching is done against the compiled form
//     rather than re-derived per pod.
//
// Conversion happens once, up front, and every mirror keeps an Original
// pointer back to the object it was built from. The compiled forms are
// immutable and safe to match against concurrently.
package repr

import (
	"fmt"
	"net/netip"

	networkingv1 "k8s.io/api/networking/v1"
)

// IPBlock is the memory-efficient mirror of a networkingv1.IPBlock.
//
// Except is stored for completeness but, matching internal/policy's
// documented simplification, is not carved out of CIDR by anything that
// consumes repr yet.
type IPBlock struct {
	CIDR   netip.Prefix
	Except []netip.Prefix
}

func newIPBlock(b *networkingv1.IPBlock) (*IPBlock, error) {
	if b == nil {
		return nil, nil
	}
	cidr, err := netip.ParsePrefix(b.CIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", b.CIDR, err)
	}
	var except []netip.Prefix
	for _, e := range b.Except {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			return nil, fmt.Errorf("invalid except CIDR %q: %w", e, err)
		}
		except = append(except, p)
	}
	return &IPBlock{CIDR: cidr, Except: except}, nil
}
