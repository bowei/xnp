# Label equivalence classes

This package is a library for efficently implementing label selection as a primitive.

The library assumes that labels and keys have been interned to a
integer value for computational efficiency.

## Performance notes

`eqc_bench_test.go` benchmarks `Add`/`Remove`/`UpdateRelevant` across synthetic
object distributions that vary partition count/size (`benchConfigs`). Two
linear scans previously dominated cost at scale and have since been replaced
with O(1) indexed lookups:

- **Label-set interning** (`internLabelSet`/`findLabelSetID`) used to find a
  `LabelSet`'s `LabelSetID` by scanning every previously interned label set
  and comparing with `slices.Equal`. Cost was O(distinct label sets seen so
  far) per object, so `Add`/`Remove` degraded to O(n²) as the number of
  distinct (relevant-projected) label sets grew — e.g. `Add/LowCardinality_4Keys`
  cost 23,137 ns/obj vs. 215 ns/obj for a config that collapses to one shared
  group. Fixed by adding `labelSetByKey map[string]LabelSetID`, keyed by a
  canonical byte encoding of the (already-sorted) `LabelSet` (`LabelSet.key`),
  making interning/lookup an O(1) map operation.
- **Object removal/lookup by ID** (`Remove`, `Update`) used to find an object
  within a namespace by scanning every partition's `objects` slice
  (`slices.DeleteFunc`/`slices.ContainsFunc`). Cost was O(objects in that
  namespace) per call, so removing every object from one large shared
  partition was O(n²) — e.g. `Remove/AllShared_1Key` cost 43,859 ns/obj, worse
  than any high-cardinality (many small partitions) config. Fixed by adding a
  `locations map[NsID]map[uint64]objLoc` index recording each object's
  partition and slice index, updated on `Add`, so `Remove`/`Update` locate an
  object in O(1) and evict it via swap-delete (`evict`) instead of a scan.
- `LabelSet.Project` rebuilt a `map[KeyID]bool` of relevant keys on every
  call, i.e. once per object added/updated/removed, even though the relevant
  key set only changes on `UpdateRelevant`. `EqC` now precomputes
  `relevantSet` once (`newRelevantSet`) and reuses it via the internal
  `EqC.project` helper, cutting one map allocation per object.

Net effect: per-object cost across all `benchConfigs` is now roughly flat
(~60-350 ns/obj) regardless of partition count/size, rather than ranging up
to tens of thousands of ns/obj on skewed distributions. Run
`go test ./internal/eqc/ -bench=. -benchmem` to reproduce.

### Allocation-churn cleanup

With the two O(n²) scans gone, `-memprofile` on `Add` pointed at the next
layer down: `LabelSet.Project` and `LabelSet.key` (an intermediate
projected-`LabelSet` allocation plus a separate byte-key allocation) were
running once *per object*, even on a cache hit against an already-interned
group — i.e. even when nothing new needed to be stored.

- `EqC.project` reuses a `relevantSet map[KeyID]bool` precomputed once in
  `New`/`UpdateRelevant`, instead of `LabelSet.Project` rebuilding that map
  on every call.
- `Add` now computes the intern-index byte key directly from an object's
  full (unprojected) `LabelSet` in one pass (`LabelSet.appendProjectKey`),
  and only materializes the actual projected `LabelSet` (`EqC.project`) the
  first time a given group is seen (`internKey`'s `isNew`) — every
  subsequent object landing in an already-seen group costs one map lookup,
  not two allocations.
- That byte key is built into a buffer reused across the whole `Add` call
  (`keyBuf[:0]` each iteration) rather than freshly allocated per object,
  and the cache-hit map lookup uses the `m[string(byteSlice)]` compiler
  idiom (https://github.com/golang/go/issues/3512) so it doesn't even pay
  for a string copy on a hit — only inserting a new group pays for that.
- `Update`'s unchanged-labels fast path (`LabelSet.projectEquals`) compares
  an object's projection against its partition's stored labels via a
  merge-walk, without materializing the projection at all.
- `internLabelSet`'s old reverse index (`map[LabelSetID]LabelSet`, kept only
  to hand out fresh IDs via `len()`) was dropped in favor of a plain
  `nextLabelSetID` counter — nothing did reverse ID→LabelSet lookups, so it
  was pure duplicate storage of content already held by `labelSetByKey`'s
  string keys and each partition's `labels`.

Effect: `AllShared_8Keys` (20,000 objects collapsing into 1 group) went from
20,172 allocs/op to 171; `AllUnique_4Keys` (every object its own group, so
some per-object allocation is unavoidable) still dropped from 8,092 to
6,091.

### Memory footprint

Speed fixes here are explicitly a memory-for-speed trade: `locations`
(`EqC`'s O(1) removal/update index) holds one entry per *live object*, not
per group, so it's the structure's memory floor regardless of how much
objects collapse into shared partitions. It's keyed on `{namespace, object
ID}` rather than nested per-namespace maps — one flat map has less baseline
overhead than many small ones when there are many namespaces, and keying on
the pair (not just ID) preserves the property that the same object ID left
behind in two namespaces (Update's documented caller-error case — see its
doc comment) is tracked independently in each, rather than one clobbering
the other's location and orphaning it un-removably.

`TestMemoryFootprint` (`go test ./internal/eqc/ -run TestMemoryFootprint
-v`) snapshots `runtime.MemStats.HeapAlloc` before/after building an `EqC`
from each `benchConfigs` distribution (after GC-settling the input objects
themselves, so the number isolates EqC's own bookkeeping) and reports
bytes retained per object:

| distinction | bytes/object |
|---|---|
| all objects collapse into 1 group (`AllShared_1Key`/`AllShared_8Keys`) | ~66-68 |
| a handful of shared groups (`ManyNamespaces_Shared`, 16 groups) | ~95 |
| every object its own group (`AllUnique_4Keys`, `HighCardinality_4Keys`) | ~290-360 |

The gap between the floor (~66) and the high-cardinality ceiling (~360) is
the `labelSetByKey` string key plus the `Partitions`/`Partition.objects`
map-and-slice entries a distinct group needs; `locations` alone accounts
for the floor. For a relevant-key selection that keeps cardinality low
(few distinct groups relative to object count), memory stays close to the
floor regardless of how many objects there are.
