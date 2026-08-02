package repr

import (
	"net/netip"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
)

func TestNewIPBlock(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		got, err := newIPBlock(nil)
		if err != nil {
			t.Fatalf("newIPBlock: %v", err)
		}
		if got != nil {
			t.Errorf("newIPBlock(nil) = %v, want nil", got)
		}
	})

	t.Run("CIDR and except", func(t *testing.T) {
		got, err := newIPBlock(&networkingv1.IPBlock{
			CIDR:   "10.0.0.0/24",
			Except: []string{"10.0.0.128/25", "10.0.0.8/29"},
		})
		if err != nil {
			t.Fatalf("newIPBlock: %v", err)
		}
		if got.CIDR != netip.MustParsePrefix("10.0.0.0/24") {
			t.Errorf("CIDR = %v, want 10.0.0.0/24", got.CIDR)
		}
		want := []netip.Prefix{netip.MustParsePrefix("10.0.0.128/25"), netip.MustParsePrefix("10.0.0.8/29")}
		if len(got.Except) != len(want) || got.Except[0] != want[0] || got.Except[1] != want[1] {
			t.Errorf("Except = %v, want %v", got.Except, want)
		}
	})

	t.Run("IPv6", func(t *testing.T) {
		got, err := newIPBlock(&networkingv1.IPBlock{CIDR: "fd00::/64"})
		if err != nil {
			t.Fatalf("newIPBlock: %v", err)
		}
		if got.CIDR != netip.MustParsePrefix("fd00::/64") {
			t.Errorf("CIDR = %v, want fd00::/64", got.CIDR)
		}
	})

	t.Run("invalid CIDR", func(t *testing.T) {
		if _, err := newIPBlock(&networkingv1.IPBlock{CIDR: "10.0.0.1"}); err == nil {
			t.Fatalf("newIPBlock: want error for a bare address, got nil")
		}
	})

	t.Run("invalid except CIDR", func(t *testing.T) {
		_, err := newIPBlock(&networkingv1.IPBlock{CIDR: "10.0.0.0/24", Except: []string{"not-a-cidr"}})
		if err == nil {
			t.Fatalf("newIPBlock: want error for invalid except CIDR, got nil")
		}
	})
}
