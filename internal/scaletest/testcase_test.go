package scaletest

import (
	"testing"

	"github.com/bowei/xnp/internal/policy"
)

func TestGenerate_Counts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Namespaces = 5
	cfg.Nodes = 4
	cfg.PodsPerNode = 10
	cfg.PoliciesPerNamespace = 3

	tc, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got, want := len(tc.Namespaces), cfg.Namespaces; got != want {
		t.Errorf("len(Namespaces) = %d, want %d", got, want)
	}
	if got, want := len(tc.Pods), cfg.Nodes*cfg.PodsPerNode; got != want {
		t.Errorf("len(Pods) = %d, want %d", got, want)
	}
	if got, want := len(tc.Policies), cfg.Namespaces*cfg.PoliciesPerNamespace; got != want {
		t.Errorf("len(Policies) = %d, want %d", got, want)
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Namespaces = 3
	cfg.Nodes = 2
	cfg.PodsPerNode = 5

	a, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(a.Pods) != len(b.Pods) || a.Pods[0].Status.PodIP != b.Pods[0].Status.PodIP {
		t.Fatalf("Generate with the same Seed produced different output")
	}
}

// TestGenerate_RendersCleanly is an end-to-end smoke test: a generated
// TestCase should always be a well-formed input to the renderer, even
// though its output isn't checked for correctness here.
func TestGenerate_RendersCleanly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Namespaces = 10
	cfg.Nodes = 8
	cfg.PodsPerNode = 20

	tc, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	in, err := policy.NewInput(tc.Policies, tc.Pods, tc.Namespaces)
	if err != nil {
		t.Fatalf("NewInput: %v", err)
	}
	if _, err := policy.RenderBasic(in); err != nil {
		t.Fatalf("Render: %v", err)
	}
}
