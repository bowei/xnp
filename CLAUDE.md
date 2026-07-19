# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`xnp` is an implementation of Kubernetes NetworkPolicy targeted at high scale. The architecture
pre-resolves policy in a central control plane and ships a compressed list of simple rules to a
lightweight per-node agent, rather than having every node re-evaluate full policy/selector logic.

Network policy computation is modeled as compiling a set of NetworkPolicies + Pods + Namespaces down
to a flat list of tuples:

```
(pod, src, dest, port, protocol, direction, verdict)
```

`cmd/xnp` is the intended server/client binary (`xnp server` / `xnp client`) but is currently a stub
(`func main() {}`). Almost all real logic lives in `internal/policy/{common,basic,indexed}` (the
renderer) and `internal/scaletest` (synthetic load generation for exercising the renderer).

## Commands

```
make build   # builds bin/xnp and bin/scaletest
make vet     # go vet ./...
make lint    # golangci-lint run (builds bin/golangci-lint from tools/go.mod first time)
make test    # go test ./...
make check   # vet + lint + test
make clean   # rm -rf bin
```

Run a single test:
```
go test ./internal/policy/indexed/ -run TestRenderIndexed_MatchesBasic -v
```

`tools/go.mod` is a separate module that pins `golangci-lint` so its huge dependency tree doesn't
pollute the main module's `go.sum`. `make lint` builds it into `bin/golangci-lint` once and rebuilds
only when the pin changes — don't add lint tooling deps to the root `go.mod`.

### Scale testing / profiling

`bin/scaletest render` generates a synthetic namespace/pod/policy set and renders it with a chosen
renderer, reporting timing, memory, and per-pod/per-node tuple-count distribution:

```
bin/scaletest render --renderer=indexed-parallel --nodes=500 --pods-per-node=200
```

`--renderer` is one of `basic`, `parallel`, `indexed`, `indexed-parallel`. Key flags: `--nodes`,
`--pods-per-node`, `--ns`, `--policies-per-ns`, `--label-cardinality` (lower = broader selector
matches), `--seed` (generation is deterministic per seed), `--workers` (for the parallel renderers).

`hack/profile.sh [-mem] [pprof-mode] [-- scaletest-render-flags...]` builds `bin/scaletest`, runs it
with `--cpuprofile` (or `--memprofile` if `-mem` is passed), and opens the result in `go tool pprof`
(default `-top`; use `-http=:0` for the interactive UI). E.g.
`hack/profile.sh -http=:0 -- --renderer=parallel --nodes=500 --pods-per-node=200` for CPU, or
`hack/profile.sh -mem -- --renderer=indexed --nodes=500 --pods-per-node=200` for a heap profile.

## Architecture: internal/policy

`internal/policy` is split into three subpackages. Four renderers (in `basic`/`indexed`) compute the
*same* tuple set from the same inputs, at increasing sophistication, and are cross-checked against
each other in differential tests — `RenderBasic` is the correctness reference; the others must never
diverge from it in output content (order may differ; tests sort before comparing).

- **`policy/common`** — code shared by every renderer, and (`common/testutil`) by every renderer's
  tests. `input.go` has `Input`/`NewInput`, "phase 1": converting raw Kubernetes
  Pods/NetworkPolicies/Namespaces into `internal/repr`'s memory-efficient mirrors, once, up front, so
  every `Render*` is a pure function of an `Input` ("phase 2") that never sees a corev1/networkingv1
  type. `render.go` has the per-pod rule-chain resolution and tuple materialization
  (`RenderPod`, `PrepareRender`, `PeerEntry`/`PortEntry`, `WriteTuples`, ...) that `basic` calls
  directly and `indexed` reuses for everything except selector evaluation. `common/testutil` (a
  separate subpackage, like stdlib's `httptest`, so `testing` never leaks into `common`'s production
  build) has fixture builders and tuple-assertion helpers shared by `basic`'s and `indexed`'s tests.
- **`policy/basic`** — `RenderBasic` (`render_basic.go`) is the brute-force reference implementation:
  for each pod, checks every policy's selector directly via `repr.CompileSelector`. O(pods ×
  selectors). `RenderParallel` (`render_parallel.go`) is the same algorithm fanned out across a worker
  pool at pod granularity; output order matches `RenderBasic` exactly.
- **`policy/indexed`** — the optimized path, implementing the strategies in `doc/optimizations.md`
  (built on `internal/repr`'s matching-layer types and `internal/eqc`'s identity classing):
  - Pods are canonicalized into small-integer **label-set identities** (`internal/eqc`,
    `eqc.LabelSetID`, adapted onto `repr` types by `render_indexed_eqc.go`) — replicas from the same
    Deployment/ReplicaSet collapse to one identity, so selector evaluation runs once per
    `(namespace, identity)` group, not once per pod.
  - Selectors are compiled once (`repr.CompileSelector`, backed by `internal/intern`) into an
    exact-match part (indexable) and a residual part (`Exists`/`NotIn`/multi-value `In`, evaluated
    only on survivors), then matched via an **inverted index** (`repr.BuildInvertedIndex`) — a
    counting join over `map[kv]→[]SelectorID` instead of re-running `labels.Selector.Matches`.
  - Label keys/values are interned to `uint32`s (`internal/intern`) so hot-path comparisons are
    integer equality and a k=v pair packs into one `uint64`.
  - `repr.NamespaceSelectorCache` memoizes `namespaceSelector` matches by canonical selector content,
    since the same selector is commonly reused across many rules.
  - Match results are stored as bitmaps (`repr.Bitset`) for O(1) membership tests.
  - Output tuple count per pod is derivable from its group template alone, so `RenderIndexed`
    (`render_indexed.go`) presizes the result slice and writes directly into it (`common.WriteTuples`)
    instead of growing via `append` — material at the scale this renders (tens of millions of tuples).
  - `RenderIndexedParallel` (`render_indexed_parallel.go`) splits `RenderIndexed` into two worker-pool
    phases instead of naive per-pod parallelism (which would just have workers race to recompute the
    same shared group templates): (1) compute each distinct `(namespace, identity)` group's
    ingress/egress template once, in parallel; (2) fan out over all pods materializing tuples from the
    shared, read-only templates, in parallel.

When touching the indexed renderers, prefer changing `common`'s shared helpers or adding a new
`doc/optimizations.md`-documented technique over ad hoc per-renderer tweaks — and add/extend the
differential test that checks the new/changed renderer's output still matches `RenderBasic`
(`policy/indexed`'s tests import `policy/basic` for exactly this).

`doc/optimizations.md` is the design doc for *why* the indexed renderer looks the way it does
(inspired by Cilium's security-identity concept and Calico Felix's labelindex); read it before
significant changes to `internal/eqc`, `internal/repr`'s matching-layer types, or `internal/intern`.

## Architecture: internal/scaletest

Generates deterministic (seeded) synthetic `Namespace`/`Pod`/`NetworkPolicy` sets sized by `Config`
(`testcase.go`) to exercise the renderers above at scale, plus reporting:

- `testcase.go` — `Generate(cfg)` builds the `TestCase`. Pod count is `Nodes * PodsPerNode`
  (independent of `Namespaces`); policy count is `Namespaces * PoliciesPerNamespace`. Node is modeled
  explicitly via `Pod.Spec.NodeName` because the per-*node* rule set (not per-pod) is what the
  lightweight agent actually has to carry — that's the thing scale testing characterizes.
- `ipalloc.go` — deterministic sequential IPv4 allocation from a CIDR, no real IPAM.
- `memtrack.go` — background heap-sampling (`TrackMemory`) used to report memory use during a render.
- `report.go` — `Summarize` turns a `TestCase` + rendered tuples + timings/mem into a `Report`,
  focused on the per-node tuple-count distribution.

`cmd/scaletest` is a Cobra CLI wrapping this package (see Commands above).

## Testing conventions

- Renderer correctness is proven primarily via **differential tests**: generate/construct the same
  policies/pods/namespaces, run two renderers, sort both tuple sets the same way, and assert equality.
  When adding a new renderer or changing an existing one's semantics, extend these rather than only
  adding standalone assertions.
- `internal/scaletest/testcase_test.go`'s `TestGenerate_RendersCleanly` feeds generated test cases
  through the renderer to catch generator/renderer integration issues, not just generator-only bugs.
- Named ports (`NetworkPolicyPort.Port` as a string) are an intentionally unsupported case — resolving
  them needs per-peer-pod container port lookups. Renderers must return an error, not a silently wrong
  tuple; there are explicit `*_NamedPortUnsupported` tests for this across all four renderers.
- `IPBlock.Except` sub-ranges are a known, documented simplification (not carved out of the CIDR).
