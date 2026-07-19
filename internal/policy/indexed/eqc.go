package indexed

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/bowei/xnp/internal/eqc"
	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/labelset"
	"github.com/bowei/xnp/internal/repr"
)

type podLabeled struct {
	p   *repr.Pod
	idx int
}

func (o *podLabeled) ID() uint64           { return podID(o.p) }
func (o *podLabeled) Namespace() eqc.NsID  { return eqc.NsID(o.p.Namespace) } // both plain uint32s
func (o *podLabeled) Labels() eqc.LabelSet { return labelSetToEqC(o.p.Labels) }

// podID derives a stable identity for a pod from its namespace/name, which
// (unlike repr.Pod, which has no numeric ID field) is guaranteed unique
// within a single render's input.
//
// TODO: this is not guarranteed to be unique.
func podID(p *repr.Pod) uint64 {
	h := fnv.New64a()
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(p.Namespace))
	h.Write(b[:])
	h.Write([]byte(p.Name))
	return h.Sum64()
}

// labelSetToEqC converts a labelset.LabelSet into eqc.LabelSet form. Both
// types maintain the same invariant (entries sorted ascending by (key,
// value)), so this is a straight element-wise conversion via
// labelset.LabelSet.At.
func labelSetToEqC(ls labelset.LabelSet) eqc.LabelSet {
	out := make(eqc.LabelSet, ls.Len())
	for i := range out {
		k, v := ls.At(i)
		out[i] = eqc.KV(eqc.KeyID(k), eqc.ValueID(v))
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
