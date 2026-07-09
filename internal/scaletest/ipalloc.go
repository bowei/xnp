package scaletest

import (
	"fmt"
	"net"
)

// ipAllocator hands out sequential, non-colliding IPv4 addresses from a
// CIDR range, so generated pods get reproducible, unique IPs without
// needing a real IPAM.
type ipAllocator struct {
	base net.IP // 4-byte form of the CIDR's network address
	n    int    // total addresses in the range
	idx  int
}

func newIPAllocator(cidr string) (*ipAllocator, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid PodCIDR %q: %w", cidr, err)
	}
	base := ipnet.IP.To4()
	if base == nil {
		return nil, fmt.Errorf("PodCIDR %q must be IPv4", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	return &ipAllocator{base: base, n: 1 << (bits - ones)}, nil
}

func (a *ipAllocator) size() int { return a.n }

// next returns the next unused address in the range. It panics if the
// range is exhausted; Generate is expected to size-check up front via
// size() so this should be unreachable in practice.
func (a *ipAllocator) next() net.IP {
	if a.idx >= a.n {
		panic("scaletest: ipAllocator exhausted")
	}
	i := a.idx
	a.idx++

	ip := append(net.IP(nil), a.base...)
	ip[0] += byte(i >> 24)
	ip[1] += byte(i >> 16)
	ip[2] += byte(i >> 8)
	ip[3] += byte(i)
	return ip
}
