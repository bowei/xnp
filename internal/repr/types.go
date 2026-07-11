// Package repr holds memory-efficient mirrors of the Kubernetes types
// internal/policy renders (Pod, NetworkPolicy, Namespace): IPs as
// netip.Addr/netip.Prefix instead of strings, and labels as an interned,
// pre-sorted form instead of map[string]string. This is storage-layer work
// only — see lookup.go for the matching-layer structures (compiled
// selectors, inverted index, bitsets) built on top of it.
package repr

import (
	"fmt"
	"net/netip"

	networkingv1 "k8s.io/api/networking/v1"
)

// kvPair packs a label key/value StringID pair into a single comparable,
// hashable, sortable value (key in the high bits so sorting by kvPair also
// sorts by key first). Used both as LabelSet's canonical form and as
// CompiledSelector's exact-match requirements (see lookup.go), so the two
// sides of a match can compare pairs directly instead of unpacking fields.
type kvPair uint64

func newKVPair(key, value StringID) kvPair {
	return kvPair(key)<<32 | kvPair(value)
}

func (p kvPair) key() StringID   { return StringID(p >> 32) }
func (p kvPair) value() StringID { return StringID(uint32(p)) }

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
