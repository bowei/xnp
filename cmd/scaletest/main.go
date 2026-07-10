// Command scaletest drives internal/scaletest from the shell, to generate
// synthetic policy/pod/namespace sets and render them at scale without
// writing a Go test.
package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"time"

	"github.com/spf13/cobra"

	"github.com/bowei/xnp/internal/policy"
	"github.com/bowei/xnp/internal/scaletest"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scaletest",
		Short: "Generate and render synthetic network policy scale tests",
	}
	cmd.AddCommand(renderCmd())
	return cmd
}

func renderCmd() *cobra.Command {
	def := scaletest.DefaultConfig()
	var cfg scaletest.Config
	var renderer string
	var workers int
	var cpuProfile string

	cmd := &cobra.Command{
		Use:   "render",
		Short: "Generate a synthetic namespace/pod/policy set and render it",
		RunE: func(cmd *cobra.Command, args []string) error {
			if renderer != "basic" && renderer != "parallel" && renderer != "indexed" {
				return fmt.Errorf("--renderer must be %q, %q, or %q, got %q", "basic", "parallel", "indexed", renderer)
			}

			if cpuProfile != "" {
				f, err := os.Create(cpuProfile)
				if err != nil {
					return fmt.Errorf("create cpu profile: %w", err)
				}
				defer f.Close()
				if err := pprof.StartCPUProfile(f); err != nil {
					return fmt.Errorf("start cpu profile: %w", err)
				}
				defer pprof.StopCPUProfile()
			}

			stopMem := scaletest.TrackMemory(50 * time.Millisecond)

			start := time.Now()
			tc, err := scaletest.Generate(cfg)
			genDur := time.Since(start)
			if err != nil {
				stopMem()
				return err
			}

			start = time.Now()
			var tuples []policy.Tuple
			switch renderer {
			case "parallel":
				tuples, err = policy.RenderParallel(tc.Policies, tc.Pods, tc.Namespaces, workers)
			case "indexed":
				tuples, err = policy.RenderIndexed(tc.Policies, tc.Pods, tc.Namespaces)
			default:
				tuples, err = policy.RenderBasic(tc.Policies, tc.Pods, tc.Namespaces)
			}
			renderDur := time.Since(start)
			mem := stopMem()
			if err != nil {
				return fmt.Errorf("render: %w", err)
			}

			report := scaletest.Summarize(tc, tuples, genDur, renderDur, mem)
			_, err = fmt.Fprint(cmd.OutOrStdout(), report.String())
			return err
		},
	}

	flags := cmd.Flags()
	flags.IntVar(&cfg.Nodes, "nodes", def.Nodes, "number of simulated nodes")
	flags.IntVar(&cfg.PodsPerNode, "pods-per-node", def.PodsPerNode, "pods generated per node")
	flags.IntVar(&cfg.Namespaces, "ns", def.Namespaces, "number of namespaces")
	flags.IntVar(&cfg.PoliciesPerNamespace, "policies-per-ns", def.PoliciesPerNamespace, "network policies generated per namespace")
	flags.Int64Var(&cfg.Seed, "seed", def.Seed, "random seed; the same seed always generates the same test case")
	flags.IntVar(&cfg.LabelCardinality, "label-cardinality", def.LabelCardinality, "distinct label values pods/namespaces are drawn from (lower = broader selector matches)")
	flags.StringVar(&cfg.PodCIDR, "pod-cidr", def.PodCIDR, "CIDR range pod IPs are allocated from")
	flags.StringVar(&renderer, "renderer", "basic", `renderer to use: "basic", "parallel", or "indexed"`)
	flags.IntVar(&workers, "workers", 0, "worker goroutines for --renderer=parallel (<=0 means runtime.GOMAXPROCS(0))")
	flags.StringVar(&cpuProfile, "cpuprofile", "", "write a CPU profile to this file")

	cfg.RulesPerPolicy = def.RulesPerPolicy
	cfg.PeersPerRule = def.PeersPerRule
	cfg.PortsPerRule = def.PortsPerRule
	cfg.IngressFraction = def.IngressFraction
	cfg.EgressFraction = def.EgressFraction
	cfg.IPBlockFraction = def.IPBlockFraction

	return cmd
}
