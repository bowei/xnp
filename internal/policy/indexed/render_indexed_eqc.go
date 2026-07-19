package indexed

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/bowei/xnp/internal/eqc"
	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/repr"
)

// This file adapts repr's storage types (repr.Pod, repr.LabelSet,
// repr.SelectorRegistry) onto internal/eqc's Labeled/LabelSet/NsID/KeyID
// types, so prepareIndexedRender can use eqc.EqC to group pods into
// (namespace, identity) partitions instead of repr's one-off
// IdentityAllocator. Kept in policy/indexed rather than repr so repr stays
// free of an eqc dependency, matching this package's existing "no
// dependency" stance.

// podLabeled adapts a *repr.Pod to eqc.Labeled. It carries idx (the pod's
// position in prepareIndexedRender's sortedPods) so callers walking
// eqc.EqC.Range() can recover which sortedPods entry an object came from
// without a reverse lookup. prepareIndexedRender allocates one []podLabeled
// backing array up front and passes eqc.EqC.Add element *pointers* into it
// (&refs[i]) rather than boxing each repr.Pod (or podLabeled value) into the
// eqc.Labeled interface individually — since *podLabeled is already a
// pointer type, that conversion is allocation-free, and the one bulk
// []podLabeled allocation replaces what would otherwise be one small heap
// allocation per pod.
type podLabeled struct {
	p   *repr.Pod
	idx int
}

func (o *podLabeled) ID() uint64           { return podID(o.p) }
func (o *podLabeled) Namespace() eqc.NsID  { return nsToEqC(o.p.Namespace) }
func (o *podLabeled) Labels() eqc.LabelSet { return labelSetToEqC(o.p.Labels) }

// podID derives a stable identity for a pod from its namespace/name, which
// (unlike repr.Pod, which has no numeric ID field) is guaranteed unique
// within a single render's input.
func podID(p *repr.Pod) uint64 {
	h := fnv.New64a()
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(p.Namespace))
	h.Write(b[:])
	h.Write([]byte(p.Name))
	return h.Sum64()
}

// nsToEqC/eqcToNS convert between repr's namespace intern.ID and eqc's NsID.
// The +1 offset avoids colliding with eqc.NoNS (0): intern.Table, like eqc's
// own ID allocators, assigns 0 to the first interned string, and a real
// namespace must never be mistaken for eqc's "no namespace" sentinel.
func nsToEqC(ns intern.ID) eqc.NsID { return eqc.NsID(ns) + 1 }
func eqcToNS(id eqc.NsID) intern.ID { return intern.ID(id - 1) }

// labelSetToEqC converts a repr.LabelSet into eqc.LabelSet form. Both types
// maintain the same invariant (entries sorted ascending by (key, value)), so
// this is a straight element-wise conversion via repr.LabelSet.At.
func labelSetToEqC(ls repr.LabelSet) eqc.LabelSet {
	out := make(eqc.LabelSet, ls.Len())
	for i := range out {
		k, v := ls.At(i)
		out[i] = eqc.KeyValue{K: eqc.KeyID(k), V: eqc.ValueID(v)}
	}
	return out
}

// referencedKeys collects every label key any compiled selector in reg
// references — the set eqc needs as its "relevant" keys, so pods that
// differ only in labels no selector ever inspects still collapse into one
// identity.
func referencedKeys(reg *repr.SelectorRegistry) []eqc.KeyID {
	seen := map[intern.ID]struct{}{}
	for _, cs := range reg.All() {
		for _, k := range cs.Keys() {
			seen[k] = struct{}{}
		}
	}
	keys := make([]eqc.KeyID, 0, len(seen))
	for k := range seen {
		keys = append(keys, eqc.KeyID(k))
	}
	return keys
}
