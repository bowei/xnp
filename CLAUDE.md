# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`xnp` is an implementation of Kubernetes NetworkPolicy targeted at high scale. The architecture
pre-resolves policy in a central control plane and ships a compressed list of simple rules to a
lightweight per-node agent, rather than having every node re-evaluate full policy/selector logic.

Network policy computation is modeled as compiling a set of NetworkPolicies + Pods + Namespaces down
to a flat list of tuples (`internal/tuple`):

```
(pod, src, dest, port, protocol, direction, verdict)
```

**State of the tree.** The original renderer (`internal/policy/{common,basic,indexed}`, plus
`internal/scaletest` and `cmd/scaletest` for synthetic load) was deleted over a long series of
commits; it computed the full tuple set from scratch on every render. What remains is being rebuilt
around an *incremental* controller: precompiled representations (`internal/repr`), label
equivalence classes (`internal/eqc`), and the live object stores (`internal/store`) that a watch
loop will update in place. `cmd/xnp` is still a `func main() {}` stub.

Two consequences: `README.md`'s "Scale testing" section and its reference to `hack/profile.sh`
describe removed code — don't trust them. And there is currently no renderer, so nothing yet turns
a store into tuples.

## Workspace layout: two modules

`go.work` at the workspace root uses `./xnp` and `./kvec`. **`kvec` is a separate git repository as
well as a separate module** (`github.com/bowei/kvec`), even though it sits alongside this one.

- `xnp/go.mod` pins a published `kvec` version, but under `go.work` the local source wins. Editing
  `kvec/` changes what `xnp` builds against immediately, with no `replace` and no version bump.
- That also means a `kvec` change is a commit in *its* repo, and `xnp` won't see it as a pinned
  dependency until the version in `xnp/go.mod` is updated. Don't leave `xnp` depending on
  uncommitted local `kvec` edits.
- `go test ./...` run from `xnp/` tests only `xnp`. Run `kvec`'s tests from `kvec/`.

## Relevant docs

- `doc/workflow.tex` — the controller design: relevant labels, pod equivalence classes, the startup
  sequence, and (still unwritten) the incremental update paths. This is the spec `internal/store`
  is being built to; read it before changing the stores. `doc/workflow.pdf` is the built artifact.
- `kvec/doc/readme.tex` — `kv` and `pred`: the public interface, the one-sided signature guarantee,
  and the internal optimizations behind them.
- `internal/repr/README.md` — one-paragraph statement of what the mirrors are for.

## Commands

```
make build   # builds bin/xnp
make vet     # go vet ./...
make lint    # golangci-lint run (builds bin/golangci-lint from tools/go.mod first time)
make test    # go test ./...
make check   # vet + lint + test
make clean   # rm -rf bin
```

Run a single test:
```
go test ./internal/store/ -run TestInitRelevantKeys -v
```

`tools/go.mod` is a separate module that pins `golangci-lint` so its huge dependency tree doesn't
pollute the main module's `go.sum`. `make lint` builds it into `bin/golangci-lint` once and rebuilds
only when the pin changes — don't add lint tooling deps to the root `go.mod`.

There is no golangci-lint config file, so it runs the default linter set.

## Architecture

Everything below `internal/` is currently pure data structures — no client-go, no watches, no
control loop yet.

- **`internal/tuple`** — the output type. Rules for a given `(Pod, Direction)` form an ordered,
  first-match-wins chain: explicit allow tuples, then one trailing wildcard carrying the default
  verdict (deny if the pod is isolated for that direction, allow if not). Protocol/Direction/Verdict
  are `uint8` enums rather than the API's strings.

- **`internal/repr`** — memory-efficient mirrors of the API types (`Pod`, `Namespace`,
  `NetworkPolicy`) carrying only the fields policy resolution needs, in the form it needs them: IPs
  as `netip.Addr`/`netip.Prefix`, labels as `kv.KV`, selectors precompiled to `*pred.Predicate`.
  Conversion happens once, up front; the compiled forms are immutable and safe to match against
  concurrently. A mirror holds no pointer back to the API object it was built from — the mirrors are
  what stays resident and the API objects are meant to be dropped — though it does share the strings
  and (for a `Pod`) the label map it copied out, which must not be mutated.

  A `Pod` is the exception to "labels as `kv.KV`": it keeps `map[string]string`, because the `KV` a
  pod is matched by is the projection onto `NetworkPolicies.RelevantKeys()` (`repr.RelevantLabels`),
  and that key set is not known at conversion time and changes as policies come and go. The raw map
  is the only form the projection can be recomputed from, and the compiled result lives once per
  `eqc.Class` rather than once per pod. A `Namespace` compiles its full label set instead: there are
  far fewer of them, and matching the full set is always correct — projecting is an optimization,
  not a change of meaning.

  Two invariants worth knowing before touching it. **Conversion rejects rather than approximates**:
  a named port, an unsupported selector operator, or an unparseable CIDR is an error at conversion
  time, not a silently wrong result later. And **a nil selector is not an empty one** — in the
  NetworkPolicy API an omitted `namespaceSelector` restricts a peer to the policy's own namespace
  while an empty one matches every namespace, so `selectorToPred` maps nil to a nil `Predicate` and
  leaves the meaning to the caller.

- **`internal/eqc`** — equivalence classes of labeled objects, grouped by namespace: objects in one
  namespace carrying equal key/value sets share a `Class`, so anything depending only on labels
  (above all, whether a selector matches) is computed once per class instead of once per object.
  Classes are bucketed by `KV.Hash()` and settled with `Equal`, since 64 bits cannot establish
  identity. Empty classes and empty namespaces are pruned as they drain. Not safe for concurrent use.

- **`internal/store`** — the controller's live view of the cluster, one type per kind:
  `NetworkPolicies` (`np.go`), `Pods` (`pods.go`), `Namespaces` (`ns.go`). All three have their
  startup `Init`; none has an incremental path yet.

  `NetworkPolicies.RelevantKeys()` is the load-bearing piece: the set of label keys named by any
  selector of any stored policy. A key no policy mentions cannot change whether a selector matches,
  so the Pod and Namespace stores will only track labels in that set. Two rules follow from it.
  Keys are read off the raw API object, not the compiled `Predicate` — `pred.Build` drops terms it
  proves redundant, but a key stays relevant as long as some selector names it. And *every*
  operator contributes, `DoesNotExist` and `NotIn` included: objects are matched against labels
  projected onto the relevant keys, so a dropped key would read as absent and satisfy an exclusion
  the real object fails.

  `Init` is the startup path (`doc/workflow.tex` §Startup). Each store builds into fresh maps and
  commits to the receiver only on success, so a failed `Init` leaves the store exactly as it was
  rather than holding half a cluster; an unconvertible object fails the whole call rather than being
  skipped, because silently dropping a policy — or a pod, which is then a pod no policy selects —
  renders as *more* allowed traffic than the cluster asked for.

  The three `Init`s are ordered: `Pods.Init` takes `NetworkPolicies.RelevantKeys()`, because a pod is
  grouped by `repr.RelevantLabels(pod.Labels, keys)` and that projection cannot be built before the
  key set is known. `Pods` owns an `eqc.Classes` and links each `repr.Pod` to its class as it
  converts; resolution is meant to walk `Pods.Classes()` rather than the pods. `Namespaces` has no
  classes — `repr.Namespace` compiles its full label set, and `doc/workflow.tex` leaves that grouping
  undone — and its `Init` cannot actually fail, returning `error` only to keep the three startup paths
  one shape.

  The incremental `Upsert`/`Delete` paths do not exist yet, and neither does the step that follows
  the three `Init`s (§Startup 4: resolve policies against pods per namespace into a resolved-policy
  cache), since there is no renderer. Two things to know before writing the incremental paths:
  removing a policy cannot just delete its keys from `relevantKeys`, since another policy may still
  name them; and a change to `relevantKeys` invalidates *every* pod's projection, so it re-groups the
  whole `Pods` store rather than one pod.

- **`kvec/pkg/kv`, `kvec/pkg/pred`** (sibling module) — what the matching hot path is built on.
  `kv.KV` is an immutable key/value set carrying two fixed 128-bit hash signatures, so `Contains`
  and `ContainsKeys` reject on a couple of word-wide `&^` tests before reading any element (the
  guarantee is one-sided: signatures reject with certainty, accept only provisionally).
  `pred.Predicate` is a conjunction of terms compiled once by `PredicateBuilder.Build`, which merges
  repeated terms per key, drops terms other terms subsume, folds single-value `In` sets into
  equalities, and records contradictions as a single `unsatisfiable` bool. Term order in `Match` is
  deliberate — cheapest and most-rejecting first — so preserve it when adding term kinds.

## Conventions

- **Tests use the standard library only** — no testify, no `go-cmp`. The house style is
  `if got, want := ...; got != want { t.Errorf("Thing() = %v, want %v", got, want) }`, with small
  fixture helpers at the top of the file.
- **Comments carry rationale, not restatement.** The dominant style throughout this codebase is a
  dense explanation of *why* a non-obvious choice was made (why a term is dropped, why an emptied
  class can't be left in place, why an error aborts rather than skips). Match it; a comment that
  only says what the next line does is out of place here.
- **Unsupported cases error, they never render.** Named ports (`NetworkPolicyPort.Port` as a string)
  need per-peer-pod container port lookups to resolve, so they are rejected at conversion. Keep new
  gaps in the same shape: fail at conversion time rather than producing a plausible-looking tuple.
- **`IPBlock.Except` sub-ranges are a known simplification** — stored, but not carved out of the
  CIDR by anything that consumes `repr`.
- **Iterators are spelled out**, `func(yield func(T) bool)` rather than `iter.Seq[T]`, matching
  `eqc` and `store`. They document that the collection must not be modified during iteration and
  that order is unspecified.
