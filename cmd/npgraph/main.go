// Command npgraph generates NetworkPolicy test data from the shape of the
// resource relationship graph -- pods, namespaces, nodes and the policies
// relating them -- and collects the same graph from a live cluster.
//
// See doc/netpol-graph.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "npgraph: %v\n", err)
		os.Exit(1)
	}
}

const usage = `npgraph builds NetworkPolicy test data from a resource graph.

Usage:
  npgraph ui [-http addr]                      run the web UI
  npgraph generate params                      write a boilerplate params file
  npgraph generate graph [-seed n] [-params f] write the graph as DOT
  npgraph generate k8s [-seed n] [-params f]   write the resources as YAML
                       [-graph f]              ... from an existing graph
  npgraph fetch [-context ctx]                 write a cluster as DOT
`

// run is main's body, taking its output streams as arguments so that the
// command surface -- which flags exist, what each subcommand writes -- is
// testable without a subprocess.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		writeString(stderr, usage)
		return fmt.Errorf("no subcommand given")
	}

	switch args[0] {
	case "ui":
		return runUI(args[1:], stderr)
	case "generate":
		return runGenerate(args[1:], stdout, stderr)
	case "fetch":
		return runFetch(args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		writeString(stdout, usage)
		return nil
	default:
		writeString(stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runUI(args []string, stderr io.Writer) error {
	fs := newFlagSet("ui", stderr)
	addr := fs.String("http", ":8080", "address and port to bind to")
	if err := fs.Parse(args); err != nil {
		return err
	}

	writeString(stderr, fmt.Sprintf("npgraph ui listening on %s\n", *addr))
	srv := &http.Server{Addr: *addr, Handler: NewUIHandler()}
	return srv.ListenAndServe()
}

func runGenerate(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		writeString(stderr, usage)
		return fmt.Errorf("generate needs one of: params, graph, k8s")
	}

	what := args[0]
	fs := newFlagSet("generate "+what, stderr)
	var (
		seed      = fs.Uint64("seed", 0, "random seed for generation")
		paramsArg = fs.String("params", "", "parameters used to generate the graph")
		graphArg  *string
	)
	if what == "k8s" {
		graphArg = fs.String("graph", "", "generate the resources from a graph file instead of from params")
	}

	switch what {
	case "params":
		// params takes no flags of its own, but parsing anyway is what turns a
		// stray -seed into an error instead of silence.
		fsParams := newFlagSet("generate params", stderr)
		if err := fsParams.Parse(args[1:]); err != nil {
			return err
		}
		return WriteParams(stdout, DefaultParams())

	case "graph":
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		g, err := generateFromParams(*paramsArg, *seed)
		if err != nil {
			return err
		}
		return WriteDOT(stdout, g)

	case "k8s":
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var (
			g   *Graph
			err error
		)
		if *graphArg != "" {
			// A graph file is the whole input: -params and -seed describe how a
			// graph would have been built, and honoring them here would mean
			// silently generating a different cluster than the file names. An
			// explicit -seed is caught by asking which flags were set rather
			// than by comparing against the default, since 0 is a seed a user
			// may well have chosen.
			for _, name := range setFlags(fs) {
				if name == "params" || name == "seed" {
					return fmt.Errorf("-graph and -%s are mutually exclusive", name)
				}
			}
			g, err = loadGraph(*graphArg)
		} else {
			g, err = generateFromParams(*paramsArg, *seed)
		}
		if err != nil {
			return err
		}
		objs, err := BuildObjects(g)
		if err != nil {
			return err
		}
		return WriteK8sYAML(stdout, objs)

	default:
		writeString(stderr, usage)
		return fmt.Errorf("unknown generate target %q", what)
	}
}

func runFetch(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("fetch", stderr)
	context := fs.String("context", "", "cluster .kube context")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cluster, err := FetchCluster(kubectlLister(*context))
	if err != nil {
		return err
	}
	g, err := GraphFromCluster(cluster)
	if err != nil {
		return err
	}
	return WriteDOT(stdout, g)
}

func generateFromParams(path string, seed uint64) (*Graph, error) {
	params, err := LoadParams(path)
	if err != nil {
		return nil, err
	}
	return Generate(params, seed)
}

func loadGraph(path string) (*Graph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseDOT(f)
}

// writeString writes to a stream whose errors there is nothing to do about: a
// failed write to stderr has nowhere left to be reported.
func writeString(w io.Writer, s string) {
	_, _ = io.WriteString(w, s)
}

// setFlags is the names of the flags actually present on the command line, as
// opposed to the ones sitting at their defaults.
func setFlags(fs *flag.FlagSet) []string {
	var names []string
	fs.Visit(func(f *flag.Flag) { names = append(names, f.Name) })
	return names
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
