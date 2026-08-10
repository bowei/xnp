package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultParamsAreValid(t *testing.T) {
	if err := DefaultParams().Validate(); err != nil {
		t.Fatalf("DefaultParams().Validate() = %v, want nil", err)
	}
}

// The boilerplate is what a user starts from, so it has to survive the trip
// through the file it is written to -- a marshalling that dropped a field would
// hand back a different experiment than the one printed.
func TestParamsRoundTrip(t *testing.T) {
	want := DefaultParams()

	var buf bytes.Buffer
	if err := WriteParams(&buf, want); err != nil {
		t.Fatalf("WriteParams() = %v, want nil", err)
	}

	got, err := ParseParams(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseParams() = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseParams(WriteParams(p)) = %+v, want %+v", got, want)
	}
}

// A params file is meant to be edited down to the parameters being varied, so
// everything absent has to fall back to the default rather than to a zero.
func TestParseParamsPartial(t *testing.T) {
	got, err := ParseParams([]byte("pods: 42\npolicies: 3\n"))
	if err != nil {
		t.Fatalf("ParseParams() = %v, want nil", err)
	}
	if got, want := got.Pods, 42; got != want {
		t.Errorf("Pods = %v, want %v", got, want)
	}
	if got, want := got.Policies, 3; got != want {
		t.Errorf("Policies = %v, want %v", got, want)
	}
	if got, want := got.PodsPerNamespace, DefaultParams().PodsPerNamespace; got != want {
		t.Errorf("PodsPerNamespace = %+v, want the default %+v", got, want)
	}
	if got, want := got.Mix, DefaultParams().Mix; got != want {
		t.Errorf("Mix = %+v, want the default %+v", got, want)
	}
}

func TestParseParamsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		// wantErr is a substring of the message, since what makes an error
		// useful here is that it names the parameter the user got wrong.
		wantErr string
	}{
		{name: "not yaml", yaml: "pods: [", wantErr: "parsing params"},
		// A typo'd key that kept the default would silently generate a
		// different experiment than the file describes.
		{name: "unknown key", yaml: "podz: 10", wantErr: "podz"},
		{name: "no pods", yaml: "pods: 0", wantErr: "pods must be > 0"},
		{name: "negative policies", yaml: "policies: -1", wantErr: "policies must be >= 0"},
		{name: "ingress fraction", yaml: "ingressFraction: 1.5", wantErr: "ingressFraction"},
		{name: "namespace peer fraction", yaml: "namespacePeerFraction: -0.1", wantErr: "namespacePeerFraction"},
		{name: "bad distribution", yaml: "inDegree: {kind: poisson}", wantErr: "inDegree"},
		{name: "reversed uniform", yaml: "outDegree: {kind: uniform, start: 10, end: 1}", wantErr: "outDegree"},
		{name: "negative mix weight", yaml: "mix: {global: -1}", wantErr: "mix.global"},
		{name: "empty mix with policies", yaml: "mix: {sameNS: 0, crossNSFocused: 0, crossNSBroad: 0, global: 0}", wantErr: "mix has no positive weight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseParams([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("ParseParams(%q) = nil error, want one", tc.yaml)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseParams(%q) error = %q, want it to mention %q", tc.yaml, err, tc.wantErr)
			}
		})
	}
}

// An empty mix is only a problem if there are policies to assign a type to.
func TestParseParamsEmptyMixNoPolicies(t *testing.T) {
	if _, err := ParseParams([]byte("policies: 0\nmix: {sameNS: 0, crossNSFocused: 0, crossNSBroad: 0, global: 0}")); err != nil {
		t.Errorf("ParseParams() = %v, want nil", err)
	}
}

func TestLoadParams(t *testing.T) {
	// No path is a legitimate way to ask for the default experiment.
	got, err := LoadParams("")
	if err != nil {
		t.Fatalf("LoadParams(\"\") = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, DefaultParams()) {
		t.Errorf("LoadParams(\"\") = %+v, want the defaults", got)
	}

	path := filepath.Join(t.TempDir(), "params.yaml")
	if err := os.WriteFile(path, []byte("pods: 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadParams(path)
	if err != nil {
		t.Fatalf("LoadParams(%q) = %v, want nil", path, err)
	}
	if got, want := got.Pods, 12; got != want {
		t.Errorf("Pods = %v, want %v", got, want)
	}

	if _, err := LoadParams(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("LoadParams(missing) = nil error, want one")
	}
}
