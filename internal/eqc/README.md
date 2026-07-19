# Label equivalence classes

This package is a library for efficiently implementing label selection as a primitive.

The library assumes that labels and keys have been interned to an
integer value for computational efficiency.

## Problem

Given a set of objects (e.g. Pods) that each carry a `LabelSet`, and a fixed
list of "relevant" label keys (e.g. the keys a NetworkPolicy's selectors can
ever reference), group the objects by the projection of their labels onto
those relevant keys. Objects whose relevant-key/value pairs are identical are
equivalent for selector-matching purposes: any selector either matches all of
them or none of them. `EqC` maintains that grouping incrementally as objects
are added, updated, and removed, and as the relevant-key set itself changes.

This is the same idea `internal/policy` calls a "label-set identity"
(`LabelSetID`) — collapsing many replica pods with identical labels down to
one group so selector evaluation runs once per group instead of once per
object.

## Data structures

- **`LabelSet`** (`types.go`) — a `[]KeyValue` of `(KeyID, ValueID)` pairs,
  kept sorted in ascending `(K, V)` order as an invariant. Sortedness lets
  `Values`, `Matches`, and `Project` all run as linear merges instead of
  per-element scans:
  - `Matches` treats the receiver as a set of *required* pairs and does a
    merge-join against a candidate set, matching Kubernetes label-selector
    semantics (the candidate may carry extra labels).
  - `Project` (and its allocation-free sibling `appendProjectKey`) filters a
    `LabelSet` down to only the relevant keys.
  - `key()` / `appendProjectKey()` encode a `LabelSet` as a fixed-width
    big-endian byte string (8 bytes per pair: 4 bytes `KeyID` + 4 bytes
    `ValueID`), giving a canonical, directly-comparable, hashable encoding
    with no delimiter-escaping concerns — used to content-address a group by
    its projected labels in O(len) with no intermediate allocation on the
    common (already-interned) path.

- **`LabelSetID`** (`types.go`, `eqc.go`) — a small dense integer assigned the
  first time a given projected `LabelSet` content is seen
  (`EqC.internKey`), analogous to `internal/policy`'s identity concept. It's
  the join key between a `Partition` and the objects that belong to it.
  Identity is content-addressed, not namespace-scoped: two namespaces whose
  objects happen to project to the same labels share a `LabelSetID`, though
  their objects still live in separate per-namespace `Partitions`.

- **`Partition`** (`eqc.go`) — one equivalence class: the shared projected
  `labels`, its `labelSetID`, and the `[]Labeled` objects currently in it.
  `Partitions` is `map[LabelSetID]Partition`, and `EqC.partitions` is
  `map[NsID]Partitions` — objects are grouped by relevant labels *within*
  each namespace, never across namespaces.

- **`labelSetByKey`** (`eqc.go`) — `map[string]LabelSetID`, the intern table
  mapping a `LabelSet.key()` byte-string to the `LabelSetID` already assigned
  to that content. Lookups pass a `[]byte` and rely on the Go compiler's
  zero-allocation `map[string(key)]` idiom, so no allocation happens except
  on the rare (one-per-distinct-group) insert path.

- **`locations`** (`eqc.go`) — `map[locKey]objLoc` where `locKey = (NsID,
  object ID)` and `objLoc = (LabelSetID, index within Partition.objects)`.
  This is what makes `Remove`/`Update` O(1) instead of O(objects): rather than
  scanning every partition to find an object, its exact slot is looked up
  directly. Keying on the `(ns, id)` pair rather than nesting per-namespace
  maps keeps this a single flat map (less overhead across many namespaces)
  while still handling the documented edge case of the same object ID
  appearing in two namespaces independently.

## Algorithms

- **Add** — for each object, project its labels onto the relevant-key set
  and intern the projection to a `LabelSetID` (`internKey`, O(len(labels))
  with an O(1) amortized map lookup). The object is appended to that
  `LabelSetID`'s `Partition` within its namespace, and `locations` records
  where it landed. Only the *first* object to establish a new group pays the
  cost of materializing the projected `LabelSet` for `Partition.labels`;
  every subsequent object in the same group reuses it.

- **Remove** — O(1) swap-and-truncate eviction (`evict`): look up the
  object's slot via `locations`, swap the last element of its `Partition`'s
  slice into that slot (fixing up the moved object's recorded index), and
  truncate. Emptied partitions/namespaces are pruned from the maps so
  iteration cost tracks live groups, not historical ones.

- **Update** — look up the object's current partition and compare its
  freshly projected labels against `Partition.labels` (`projectEquals`, which
  checks equality without materializing the projection). If unchanged, the
  object is swapped in place at its existing slot — the common case, and the
  reason `Partition.Labels()` is documented as "the projection frozen at Add
  time": it lets Update detect drift without needing the object's prior
  label state. If the projection changed, the object is evicted and
  re-added, which may land it in a new or newly-created group.

- **UpdateRelevant** — changing which keys are relevant can both merge
  partitions (a dropped key may make two previously-distinct groups project
  identically) and split them (an added key may distinguish previously-
  identical objects). Rather than incrementally reconciling, this collects
  every currently-tracked object, resets all indexes (`partitions`,
  `labelSetByKey`, `locations`, the `LabelSetID` counter), and re-`Add`s
  everything under the new projection — O(objects), but correctness-simple
  and infrequent (the relevant-key set changes only when policy-referenced
  label keys change, not per object).

## Complexity summary

| Operation | Cost |
|---|---|
| `Add` (n objects) | O(n · k) where k = labels per object |
| `Remove` / per-object `Update` (no group change) | O(1) amortized |
| `Update` (group change) | O(k) (evict + re-add) |
| `UpdateRelevant` | O(N · k) over all N tracked objects |

## Possible future improvement

`Partition.objects` is a plain `[]Labeled`. A noted future direction is to
back partitions with Roaring bitmaps instead, once objects are represented by
dense integer IDs, to shrink memory further at very large object counts.
