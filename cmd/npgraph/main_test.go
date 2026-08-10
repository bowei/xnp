package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI drives the command surface the way main does, minus the process.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errBuf bytes.Buffer
	err = run(args, &out, &errBuf)
	return out.String(), errBuf.String(), err
}

func TestRunGenerateParams(t *testing.T) {
	stdout, _, err := runCLI(t, "generate", "params")
	if err != nil {
		t.Fatalf("generate params = %v, want nil", err)
	}

	// The boilerplate is only useful if it is a params file: it goes straight
	// back in through -params.
	got, err := ParseParams([]byte(stdout))
	if err != nil {
		t.Fatalf("the boilerplate does not parse as params: %v\n%s", err, stdout)
	}
	if got, want := got.Pods, DefaultParams().Pods; got != want {
		t.Errorf("pods = %v, want %v", got, want)
	}
}

func TestRunGenerateGraph(t *testing.T) {
	path := writeParams(t, "pods: 20\npodsPerNamespace: {kind: constant, value: 5}\npolicies: 2\n")

	stdout, _, err := runCLI(t, "generate", "graph", "-seed", "5", "-params", path)
	if err != nil {
		t.Fatalf("generate graph = %v, want nil", err)
	}

	g, err := ParseDOT(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("the emitted graph does not parse: %v\n%s", err, stdout)
	}
	if got, want := len(g.Pods), 20; got != want {
		t.Errorf("len(Pods) = %v, want %v", got, want)
	}
	if got, want := len(g.Policies), 2; got != want {
		t.Errorf("len(Policies) = %v, want %v", got, want)
	}

	// -seed is the reproducibility contract, and the only way to see that it is
	// wired through is to run the command twice.
	again, _, err := runCLI(t, "generate", "graph", "-seed", "5", "-params", path)
	if err != nil {
		t.Fatalf("generate graph = %v, want nil", err)
	}
	if again != stdout {
		t.Error("generate graph with the same seed produced different output")
	}
	other, _, err := runCLI(t, "generate", "graph", "-seed", "6", "-params", path)
	if err != nil {
		t.Fatalf("generate graph = %v, want nil", err)
	}
	if other == stdout {
		t.Error("generate graph ignored -seed")
	}
}

// No -params is the default experiment rather than an error.
func TestRunGenerateGraphNoParams(t *testing.T) {
	stdout, _, err := runCLI(t, "generate", "graph", "-seed", "1")
	if err != nil {
		t.Fatalf("generate graph = %v, want nil", err)
	}
	if !strings.Contains(stdout, "digraph npgraph") {
		t.Errorf("generate graph produced no graph:\n%s", stdout)
	}
}

func TestRunGenerateK8s(t *testing.T) {
	path := writeParams(t, "pods: 10\npodsPerNamespace: {kind: constant, value: 5}\npolicies: 1\n")

	stdout, _, err := runCLI(t, "generate", "k8s", "-seed", "2", "-params", path)
	if err != nil {
		t.Fatalf("generate k8s = %v, want nil", err)
	}
	for _, want := range []string{"kind: Namespace", "kind: Pod", "kind: NetworkPolicy"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("generate k8s output does not contain %q", want)
		}
	}
}

// The two halves of the workflow have to meet: a graph written by `generate
// graph` is what `generate k8s -graph` reads, and the resources it produces
// have to be the ones the params would have produced directly.
func TestRunGenerateK8sFromGraphFile(t *testing.T) {
	params := writeParams(t, "pods: 10\npodsPerNamespace: {kind: constant, value: 5}\npolicies: 2\n")

	dot, _, err := runCLI(t, "generate", "graph", "-seed", "4", "-params", params)
	if err != nil {
		t.Fatalf("generate graph = %v, want nil", err)
	}
	graphPath := filepath.Join(t.TempDir(), "graph.dot")
	if err := os.WriteFile(graphPath, []byte(dot), 0o600); err != nil {
		t.Fatal(err)
	}

	fromGraph, _, err := runCLI(t, "generate", "k8s", "-graph", graphPath)
	if err != nil {
		t.Fatalf("generate k8s -graph = %v, want nil", err)
	}
	fromParams, _, err := runCLI(t, "generate", "k8s", "-seed", "4", "-params", params)
	if err != nil {
		t.Fatalf("generate k8s -params = %v, want nil", err)
	}
	if fromGraph != fromParams {
		t.Error("generate k8s -graph and generate k8s -params disagree for the same graph")
	}
}

func TestRunErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no subcommand", args: nil, wantErr: "no subcommand"},
		{name: "unknown subcommand", args: []string{"frobnicate"}, wantErr: "unknown subcommand"},
		{name: "generate with no target", args: []string{"generate"}, wantErr: "generate needs one of"},
		{name: "unknown generate target", args: []string{"generate", "pods"}, wantErr: "unknown generate target"},
		{name: "unknown flag", args: []string{"generate", "graph", "-nope"}, wantErr: "flag provided but not defined"},
		// generate params takes no flags, and a -seed passed to it is a
		// misunderstanding worth reporting rather than ignoring.
		{name: "flag on params", args: []string{"generate", "params", "-seed", "1"}, wantErr: "flag provided but not defined"},
		{name: "missing params file", args: []string{"generate", "graph", "-params", missing}, wantErr: "no such file"},
		{name: "missing graph file", args: []string{"generate", "k8s", "-graph", missing}, wantErr: "no such file"},
		{
			// A graph file is the whole input; honoring -params alongside it
			// would silently generate a different cluster than the file names.
			name:    "graph and params together",
			args:    []string{"generate", "k8s", "-graph", missing, "-params", missing},
			wantErr: "mutually exclusive",
		},
		{name: "invalid params", args: []string{"generate", "graph", "-params", writeParams(t, "pods: 0")}, wantErr: "pods must be > 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("run(%v) = nil error, want one mentioning %q", tc.args, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("run(%v) = %q, want it to mention %q", tc.args, err, tc.wantErr)
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	stdout, _, err := runCLI(t, "help")
	if err != nil {
		t.Fatalf("help = %v, want nil", err)
	}
	for _, want := range []string{"npgraph ui", "npgraph generate params", "npgraph fetch"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not document %q", want)
		}
	}

	// A user who gets the invocation wrong gets the usage on stderr, not just a
	// one-line error.
	_, stderr, err := runCLI(t, "frobnicate")
	if err == nil {
		t.Fatal("run(frobnicate) = nil error, want one")
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr does not contain the usage:\n%s", stderr)
	}
}

func writeParams(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "params.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
