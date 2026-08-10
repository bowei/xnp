package main

import (
	"fmt"
	"io"
	"os"

	"sigs.k8s.io/yaml"
)

// Params is the full description of a family of graphs. Everything the
// generator does is a function of these and a seed, so a params file plus a
// seed is a reproducible experiment.
type Params struct {
	// Pods is the total number of pods in the cluster (P in the doc). It is a
	// count and not a distribution: it is the size of the experiment, and the
	// thing every other parameter is expressed relative to.
	Pods int `json:"pods"`

	// PodsPerNamespace and PodsPerNode are drawn once per namespace and once
	// per node respectively, and consume pods until Pods runs out. So neither
	// the number of namespaces nor the number of nodes is set directly: they
	// fall out of the total and the size distribution, which is how a real
	// cluster works.
	PodsPerNamespace Distribution `json:"podsPerNamespace"`
	PodsPerNode      Distribution `json:"podsPerNode"`

	// Policies is the number of NetworkPolicy vertices (NetPol in the doc).
	Policies int `json:"policies"`

	// InDegree is the number of pods a policy applies to, drawn per policy and
	// clamped to the size of the policy's namespace.
	InDegree Distribution `json:"inDegree"`
	// OutDegree is the number of pods a policy names as peers, drawn per
	// policy.
	OutDegree Distribution `json:"outDegree"`

	// IngressFraction is the share of policies that are ingress; the rest are
	// egress.
	IngressFraction float64 `json:"ingressFraction"`

	// Mix is how the policies are split across the four types.
	Mix PolicyMix `json:"mix"`

	// FocusedNamespaces and BroadNamespaces are the number of namespaces a
	// CrossNSFocused and a CrossNSBroad policy's Out set spans. They are
	// clamped to their respective sides of FocusedNamespaceLimit, since that
	// boundary is what distinguishes the two types.
	FocusedNamespaces Distribution `json:"focusedNamespaces"`
	BroadNamespaces   Distribution `json:"broadNamespaces"`

	// NamespacePeerFraction is the share of policies whose peers are whole
	// namespaces (a peer with a namespaceSelector and no podSelector) rather
	// than individual pods. The two produce very different work for a renderer
	// at the same out-degree -- one peer standing for a thousand pods against a
	// thousand peers -- so the mix between them is worth varying.
	NamespacePeerFraction float64 `json:"namespacePeerFraction"`
}

// PolicyMix is the fraction of policies of each type. The four are normalized
// against their sum rather than required to add to one: a user editing the file
// to try "twice as many global policies" should be able to write a 2 without
// rebalancing the other three.
type PolicyMix struct {
	SameNS         float64 `json:"sameNS"`
	CrossNSFocused float64 `json:"crossNSFocused"`
	CrossNSBroad   float64 `json:"crossNSBroad"`
	Global         float64 `json:"global"`
}

// weights returns the mix in the order allocate is called with, which is the
// order policyTypes lists the types in.
func (m PolicyMix) weights() []float64 {
	return []float64{m.SameNS, m.CrossNSFocused, m.CrossNSBroad, m.Global}
}

// policyTypes is the canonical order of the four types, shared by the mix
// weights and the allocation of policies to types.
var policyTypes = []PolicyType{PolicySameNS, PolicyCrossNSFocused, PolicyCrossNSBroad, PolicyGlobal}

// DefaultParams is the boilerplate `npgraph generate params` writes: a cluster
// of a size worth measuring, with every distribution kind used at least once so
// the file doubles as documentation of what can be written where.
func DefaultParams() *Params {
	return &Params{
		Pods:             1000,
		PodsPerNamespace: Uniform(5, 20),
		PodsPerNode:      Constant(30),

		Policies:  100,
		InDegree:  Uniform(1, 10),
		OutDegree: Gaussian(10, 3),

		IngressFraction: 0.5,
		Mix: PolicyMix{
			SameNS:         0.5,
			CrossNSFocused: 0.3,
			CrossNSBroad:   0.15,
			Global:         0.05,
		},
		FocusedNamespaces: Uniform(2, 10),
		BroadNamespaces:   Uniform(11, 30),

		NamespacePeerFraction: 0.1,
	}
}

// Validate reports the first parameter that cannot produce a graph. It does not
// range-check the counts a distribution can draw -- a negative out-degree is a
// legitimate way to say "usually zero, sometimes a few" for a Gaussian -- since
// the generator clamps every draw to what the graph can supply.
func (p *Params) Validate() error {
	if p.Pods <= 0 {
		return fmt.Errorf("pods must be > 0, got %d", p.Pods)
	}
	if p.Policies < 0 {
		return fmt.Errorf("policies must be >= 0, got %d", p.Policies)
	}
	if p.IngressFraction < 0 || p.IngressFraction > 1 {
		return fmt.Errorf("ingressFraction must be in [0,1], got %v", p.IngressFraction)
	}
	if p.NamespacePeerFraction < 0 || p.NamespacePeerFraction > 1 {
		return fmt.Errorf("namespacePeerFraction must be in [0,1], got %v", p.NamespacePeerFraction)
	}

	for name, d := range map[string]Distribution{
		"podsPerNamespace":  p.PodsPerNamespace,
		"podsPerNode":       p.PodsPerNode,
		"inDegree":          p.InDegree,
		"outDegree":         p.OutDegree,
		"focusedNamespaces": p.FocusedNamespaces,
		"broadNamespaces":   p.BroadNamespaces,
	} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	for name, w := range map[string]float64{
		"mix.sameNS":         p.Mix.SameNS,
		"mix.crossNSFocused": p.Mix.CrossNSFocused,
		"mix.crossNSBroad":   p.Mix.CrossNSBroad,
		"mix.global":         p.Mix.Global,
	} {
		if w < 0 {
			return fmt.Errorf("%s must be >= 0, got %v", name, w)
		}
	}
	if p.Policies > 0 && p.Mix.SameNS+p.Mix.CrossNSFocused+p.Mix.CrossNSBroad+p.Mix.Global <= 0 {
		return fmt.Errorf("mix has no positive weight, so the %d policies cannot be assigned a type", p.Policies)
	}

	return nil
}

// ParseParams reads a params file. The defaults show through for anything the
// file leaves out, so a user can write the three parameters they are varying
// rather than the whole document.
func ParseParams(data []byte) (*Params, error) {
	p := DefaultParams()
	// Unknown fields are rejected: the whole point of the file is to control
	// the experiment, and a typo'd key that silently kept the default would
	// produce a graph nobody asked for and no error to say so.
	if err := yaml.UnmarshalStrict(data, p); err != nil {
		return nil, fmt.Errorf("parsing params: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadParams reads a params file from disk, or returns the defaults if path is
// empty -- `generate graph` with no -params is a legitimate way to ask for the
// default experiment.
func LoadParams(path string) (*Params, error) {
	if path == "" {
		return DefaultParams(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseParams(data)
}

// WriteParams writes p as YAML.
func WriteParams(w io.Writer, p *Params) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
