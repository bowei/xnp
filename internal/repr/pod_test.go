package repr

import (
	"net/netip"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "a",
			Labels:    map[string]string{"app": "web"},
		},
		Status: corev1.PodStatus{PodIP: "10.0.0.1"},
	}

	got, err := NewPod(pod)
	if err != nil {
		t.Fatalf("NewPod: %v", err)
	}
	if got.Namespace != "ns" || got.Name != "a" {
		t.Errorf("Namespace/Name = %q/%q, want \"ns\"/\"a\"", got.Namespace, got.Name)
	}
	if v, ok := got.Labels.Get("app"); !ok || v != "web" {
		t.Errorf("Labels[app] = %q, %v, want \"web\", true", v, ok)
	}
	if want := []netip.Addr{netip.MustParseAddr("10.0.0.1")}; !slices.Equal(got.IPs, want) {
		t.Errorf("IPs = %v, want %v", got.IPs, want)
	}
	if got.Original != pod {
		t.Errorf("Original = %p, want %p", got.Original, pod)
	}
}

func TestNewPod_IPs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status corev1.PodStatus
		want   []netip.Addr
	}{
		{
			name: "dual-stack PodIPs takes precedence over PodIP",
			status: corev1.PodStatus{
				PodIP:  "10.0.0.1",
				PodIPs: []corev1.PodIP{{IP: "10.0.0.1"}, {IP: "fd00::1"}},
			},
			want: []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("fd00::1")},
		},
		{
			name:   "no IP assigned yet",
			status: corev1.PodStatus{},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewPod(&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a"},
				Status:     tc.status,
			})
			if err != nil {
				t.Fatalf("NewPod: %v", err)
			}
			if !slices.Equal(got.IPs, tc.want) {
				t.Errorf("IPs = %v, want %v", got.IPs, tc.want)
			}
		})
	}
}

func TestNewPod_InvalidIP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status corev1.PodStatus
	}{
		{name: "PodIP", status: corev1.PodStatus{PodIP: "not-an-ip"}},
		{name: "PodIPs", status: corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: "10.0.0.1"}, {IP: "nope"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPod(&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a"},
				Status:     tc.status,
			})
			if err == nil {
				t.Fatalf("NewPod: want error for invalid IP, got nil")
			}
		})
	}
}
