package selector

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"

	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/labels"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hashLabelKV(sorted []intern.Pair) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, x := range sorted {
		binary.LittleEndian.PutUint64(b[:], uint64(x))
		h.Write(b[:])
	}
	return h.Sum64()
}

func labelKVEqual(a, b []intern.Pair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SelectorID is a dense integer identity for a compiled, deduped selector.
type SelectorID uint32

type residualOp int

const (
	residualDoesNotExist residualOp = iota
	residualIn                      // multi-value In
	residualNotIn
)

// residualReq is a selector requirement that can't be answered by presence
// in an inverted index and must be evaluated directly against a candidate
// label set.
type residualReq struct {
	key    intern.ID
	op     residualOp
	values map[intern.ID]struct{}
}

// CompiledSelector is a metav1.LabelSelector compiled once into interned
// integer form, split into:
//
//   - exact: k=v requirements (MatchLabels, and single-value In) that are
//     indexable via a simple inverted index.
//   - existsKeys: Exists requirements, also indexable (by key alone).
//   - residual: DoesNotExist/NotIn/multi-value-In, evaluated only on
//     candidates that already cleared the indexed part.
type CompiledSelector struct {
	exact      []intern.Pair // sorted
	existsKeys []intern.ID
	residual   []residualReq
}

// positiveCount is the number of indexable positive requirements (exact +
// Exists) a matching label set must satisfy.
func (cs CompiledSelector) positiveCount() int {
	return len(cs.exact) + len(cs.existsKeys)
}

// Keys returns every label key cs references, across its exact, Exists, and
// residual (NotIn/DoesNotExist/multi-value-In) requirements, deduplicated.
// Lets external packages determine which keys are "relevant" to selector
// matching without depending on repr's internal compiled form.
func (cs CompiledSelector) Keys() []intern.ID {
	seen := make(map[intern.ID]struct{}, len(cs.exact)+len(cs.existsKeys)+len(cs.residual))
	for _, e := range cs.exact {
		seen[e.Key()] = struct{}{}
	}
	for _, k := range cs.existsKeys {
		seen[k] = struct{}{}
	}
	for _, r := range cs.residual {
		seen[r.key] = struct{}{}
	}
	keys := make([]intern.ID, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

// CompileSelector converts sel into compiled form, interning every key/value
// it references into tbl.
func CompileSelector(sel metav1.LabelSelector, tbl *intern.Table) (CompiledSelector, error) {
	var cs CompiledSelector

	for k, v := range sel.MatchLabels {
		cs.exact = append(cs.exact, intern.NewPair(tbl.Intern(k), tbl.Intern(v)))
	}

	for _, expr := range sel.MatchExpressions {
		key := tbl.Intern(expr.Key)
		switch expr.Operator {
		case metav1.LabelSelectorOpIn:
			if len(expr.Values) == 0 {
				return CompiledSelector{}, fmt.Errorf("selector: In requires at least one value for key %q", expr.Key)
			}
			if len(expr.Values) == 1 {
				cs.exact = append(cs.exact, intern.NewPair(key, tbl.Intern(expr.Values[0])))
				continue
			}
			values := make(map[intern.ID]struct{}, len(expr.Values))
			for _, v := range expr.Values {
				values[tbl.Intern(v)] = struct{}{}
			}
			cs.residual = append(cs.residual, residualReq{key: key, op: residualIn, values: values})
		case metav1.LabelSelectorOpNotIn:
			if len(expr.Values) == 0 {
				return CompiledSelector{}, fmt.Errorf("selector: NotIn requires at least one value for key %q", expr.Key)
			}
			values := make(map[intern.ID]struct{}, len(expr.Values))
			for _, v := range expr.Values {
				values[tbl.Intern(v)] = struct{}{}
			}
			cs.residual = append(cs.residual, residualReq{key: key, op: residualNotIn, values: values})
		case metav1.LabelSelectorOpExists:
			cs.existsKeys = append(cs.existsKeys, key)
		case metav1.LabelSelectorOpDoesNotExist:
			cs.residual = append(cs.residual, residualReq{key: key, op: residualDoesNotExist})
		default:
			return CompiledSelector{}, fmt.Errorf("selector: unsupported operator %q", expr.Operator)
		}
	}

	slices.Sort(cs.exact)
	slices.Sort(cs.existsKeys)
	return cs, nil
}

// Matches is the full (exact + Exists + residual) evaluation of cs against
// ls. It's the ground-truth semantics; InvertedIndex is an optimization of
// exactly this check at scale.
func (cs CompiledSelector) Matches(ls labels.Set) bool {
	for _, e := range cs.exact {
		if v, ok := ls.Get(e.Key()); !ok || v != e.Value() {
			return false
		}
	}
	for _, k := range cs.existsKeys {
		if _, ok := ls.Get(k); !ok {
			return false
		}
	}
	return cs.matchesResidual(ls)
}

func (cs CompiledSelector) matchesResidual(ls labels.Set) bool {
	for _, r := range cs.residual {
		v, present := ls.Get(r.key)
		switch r.op {
		case residualDoesNotExist:
			if present {
				return false
			}
		case residualIn:
			if !present {
				return false
			}
			if _, ok := r.values[v]; !ok {
				return false
			}
		case residualNotIn:
			if present {
				if _, ok := r.values[v]; ok {
					return false
				}
			}
		}
	}
	return true
}

// canonicalKey produces a string that's identical for two
// metav1.LabelSelector values with the same meaning regardless of map/slice
// iteration order, so SelectorRegistry and NamespaceSelectorCache can dedupe
// repeated identical selectors.
func canonicalKey(sel metav1.LabelSelector) string {
	keys := make([]string, 0, len(sel.MatchLabels))
	for k := range sel.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(sel.MatchLabels[k])
		b.WriteByte(',')
	}

	exprs := append([]metav1.LabelSelectorRequirement(nil), sel.MatchExpressions...)
	sort.Slice(exprs, func(i, j int) bool {
		if exprs[i].Key != exprs[j].Key {
			return exprs[i].Key < exprs[j].Key
		}
		return exprs[i].Operator < exprs[j].Operator
	})
	for _, e := range exprs {
		b.WriteString(e.Key)
		b.WriteByte(':')
		b.WriteString(string(e.Operator))
		b.WriteByte(':')
		values := append([]string(nil), e.Values...)
		sort.Strings(values)
		b.WriteString(strings.Join(values, "|"))
		b.WriteByte(';')
	}
	return b.String()
}

// SelectorRegistry compiles metav1.LabelSelectors exactly once, deduping
// identical selectors by their canonical content.
type SelectorRegistry struct {
	tbl      *intern.Table
	byKey    map[string]SelectorID
	compiled []CompiledSelector
}

func NewSelectorRegistry(tbl *intern.Table) *SelectorRegistry {
	return &SelectorRegistry{tbl: tbl, byKey: map[string]SelectorID{}}
}

func (r *SelectorRegistry) Get(sel metav1.LabelSelector) (SelectorID, error) {
	key := canonicalKey(sel)
	if id, ok := r.byKey[key]; ok {
		return id, nil
	}
	cs, err := CompileSelector(sel, r.tbl)
	if err != nil {
		return 0, err
	}
	id := SelectorID(len(r.compiled))
	r.compiled = append(r.compiled, cs)
	r.byKey[key] = id
	return id, nil
}

// Compiled returns the compiled selector for id.
func (r *SelectorRegistry) Compiled(id SelectorID) CompiledSelector {
	return r.compiled[id]
}

// All returns every compiled selector registered so far, indexed by
// SelectorID.
func (r *SelectorRegistry) All() []CompiledSelector {
	return r.compiled
}
