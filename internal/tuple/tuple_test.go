package tuple

import (
	"fmt"
	"maps"
	"net/netip"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/eqc"
)

func class(ns, name string, labels map[string]string) *eqc.Class {
	var cl eqc.Classes
	return cl.Add(eqc.Entry{
		Namespace: ns,
		Name:      name,
		KV:        kv.NewKV(maps.All(labels), len(labels)),
	})
}

func prefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q) = %v", s, err)
	}
	return p
}

func port(v int32) *int32 { return &v }

func TestTupleString(t *testing.T) {
	web := class("default", "pod-a", map[string]string{"app": "web"})
	webStr := fmt.Sprintf("default/eqc:%016x", web.KV().Hash())

	for _, tc := range []struct {
		name string
		tup  Tuple
		want string
	}{
		{
			name: "class to cidr over a port range",
			tup: Tuple{
				Source:    web,
				DestCIDR:  prefix(t, "10.0.0.0/8"),
				Port:      port(80),
				EndPort:   port(443),
				Protocol:  ProtocolTCP,
				Direction: DirectionEgress,
				Verdict:   VerdictAllow,
			},
			want: "egress allow " + webStr + " -> 10.0.0.0/8 tcp:80-443",
		},
		{
			name: "cidr to class on a single port",
			tup: Tuple{
				SourceCIDR: prefix(t, "192.168.0.0/16"),
				Dest:       web,
				Port:       port(53),
				Protocol:   ProtocolUDP,
				Direction:  DirectionIngress,
				Verdict:    VerdictAllow,
			},
			want: "ingress allow 192.168.0.0/16 -> " + webStr + " udp:53",
		},
		{
			// The trailing wildcard of a chain: no endpoints, no ports, and so
			// no protocol either.
			name: "wildcard deny",
			tup: Tuple{
				Protocol:  ProtocolTCP,
				Direction: DirectionIngress,
				Verdict:   VerdictDeny,
			},
			want: "ingress deny any -> any any",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := tc.tup.String(), tc.want; got != want {
				t.Errorf("String() = %v, want %v", got, want)
			}
		})
	}
}
