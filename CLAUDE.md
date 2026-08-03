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

## Relevant docs

`docs/workflow.tex` describes the algorithms at a high level.

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
