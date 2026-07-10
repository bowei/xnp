# Optimizations

## Label optimizations

1. Identity/equivalence classes on the pod side, not just the selector side

Applying it to label sets rather than selectors: replicas from the same ReplicaSet/Deployment have identical labels, so a 5,000-pod cluster might have only 200 distinct label sets. Canonicalize each pod's labels (sorted k=v joined, or a hash of that), and evaluate selectors once per unique label set, then fan the result out to all pods sharing it.
gotype LabelSetID uint32 // interned identity for a canonical label set

type identityAllocator struct {
    byHash map[uint64]LabelSetID
    sets   []labels.Set
}

This is Cilium's "security identity" concept. Match results become a relation between selector IDs and label-set IDs — both small integer spaces — instead of selectors × pods.

1. Split selectors into exact-match and residual parts
Most real-world selectors are pure matchLabels (or In with one value), which are conjunctions of exact key=value requirements. Decompose each selector:

Exact part: set of required k=v pairs → indexable
Residual part: Exists, DoesNotExist, NotIn, multi-value In → evaluated only on candidates that survive the exact part

For selectors that are 100% exact-match and whose keys fully cover a discriminating key (see below), matching degenerates into your hash-lookup idea.

3. Inverted index on label key/value pairs
Maintain map[kvPair]set[SelectorID] — for each k=v, which selectors require it. To find selectors matching a pod:
go// counting join: a selector with N exact requirements matches
// iff all N of its kv pairs are present in the pod's labels
counts := map[SelectorID]int{}
for k, v := range podLabels {
    for sel := range index[kvPair{k, v}] {
        counts[sel]++
    }
}
for sel, c := range counts {
    if c == requiredCount[sel] && residualMatches(sel, podLabels) {
        // match
    }
}

This makes cost proportional to the pod's label count × selectors-per-label, not total selector count. Calico's Felix does essentially this in its labelindex package. Handle Exists requirements by indexing on the key alone; handle selectors with zero positive requirements (e.g. empty selector, or pure NotIn) in a small "scan-always" bucket — they're rare.

4. Pick a discriminating key for a first-level partition
If nearly every selector constrains app or app.kubernetes.io/name, partition the index by that key's value first: map[appValue][]SelectorID. Pods jump straight to a tiny bucket. You can detect the best partition key dynamically by counting which key appears in the most selectors.

5. Namespace partitioning (free and mandatory)
A NetworkPolicy's spec.podSelector only matches pods in its own namespace, so keep one index per namespace — this alone often cuts the problem by 10–100×. Peer namespaceSelectors need a second-level structure: index namespaces by their labels (same machinery, tiny scale), producing a set of namespace IDs, then intersect with per-namespace pod matches. namespaceSelector + podSelector combined peers become a cross-product over two small precomputed sets.

6. Precompile and intern everything

Convert metav1.LabelSelector → labels.Selector exactly once (LabelSelectorAsSelector allocates and sorts on every call).
Better: skip labels.Selector.Matches entirely in the hot path. Its generic requirement-walking does string comparisons and switch dispatch. Compile to your own struct of []exactReq + []residualReq with interned string IDs.
Intern label keys and values into uint32s via a global table. Comparisons become integer equality, kv pairs become a single uint64 (key<<32 | value), and your inverted-index map keys get cheap and allocation-free.

8. Bitmaps for the match relation
Once selectors and label-set identities are dense integer IDs, store results as bitmaps (RoaringBitmap or plain []uint64): matchedIdentities[SelectorID] = bitmap. Set operations you need downstream — "pods selected by policy X's podSelector ∩ affected by ingress rule Y" — become fast bitwise ANDs, and memory stays compact.

## Delta-updates

Incremental (delta-based) recomputation

Never recompute the full relation on events:

Pod label change: diff old/new label sets. Only selectors touching the changed kv pairs (found via the inverted index) can change verdict; re-evaluate just those.
Pod add/delete with unchanged labels (the common churn case — rolling updates): if the label set identity already exists, just add/remove the pod from the identity's membership set. Zero selector evaluations.
Policy add/delete: evaluate the new selector against unique label sets (not pods), using the discriminating-key partition to limit candidates.

## Caching

Cache negative/positive verdicts by (selectorHash, labelSetHash)
If you can't fully restructure into indexes, a memo table map[uint64]bool keyed by hash(selector) ^ hash(labelSet) is a cheap 80% solution, since both spaces are small in practice. Evict on any interned-table growth or just bound with an LRU.
