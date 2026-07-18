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
