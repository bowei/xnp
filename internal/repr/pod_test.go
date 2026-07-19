package repr

import (
	"net/netip"
	"testing"

	"github.com/bowei/xnp/internal/intern"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewPod(t *testing.T) {
	tbl := intern.NewTable()

	t.Run("single IP", func(t *testing.T) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a", Labels: map[string]string{"app": "web"}},
			Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
		}
		got, err := NewPod(p, tbl)
		if err != nil {
			t.Fatalf("NewPod: %v", err)
		}
		if got.Name != "a" {
			t.Errorf("Name = %q, want \"a\"", got.Name)
		}
		if tbl.String(got.Namespace) != "ns" {
			t.Errorf("Namespace = %q, want \"ns\"", tbl.String(got.Namespace))
		}
		want := netip.MustParseAddr("10.0.0.1")
		if len(got.IPs) != 1 || got.IPs[0] != want {
			t.Errorf("IPs = %v, want [%v]", got.IPs, want)
		}
	})

	t.Run("dual-stack PodIPs takes precedence over PodIP", func(t *testing.T) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"},
			Status: corev1.PodStatus{
				PodIP:  "10.0.0.1",
				PodIPs: []corev1.PodIP{{IP: "10.0.0.1"}, {IP: "fd00::1"}},
			},
		}
		got, err := NewPod(p, tbl)
		if err != nil {
			t.Fatalf("NewPod: %v", err)
		}
		want := []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("fd00::1")}
		if len(got.IPs) != len(want) || got.IPs[0] != want[0] || got.IPs[1] != want[1] {
			t.Errorf("IPs = %v, want %v", got.IPs, want)
		}
	})

	t.Run("no IP assigned yet", func(t *testing.T) {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "c"}}
		got, err := NewPod(p, tbl)
		if err != nil {
			t.Fatalf("NewPod: %v", err)
		}
		if got.IPs != nil {
			t.Errorf("IPs = %v, want nil", got.IPs)
		}
	})

	t.Run("invalid IP errors", func(t *testing.T) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "d"},
			Status:     corev1.PodStatus{PodIP: "not-an-ip"},
		}
		if _, err := NewPod(p, tbl); err == nil {
			t.Fatalf("NewPod: want error for invalid IP, got nil")
		}
	})
}
