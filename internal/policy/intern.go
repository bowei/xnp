package policy

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// symbolTable interns label keys and values into dense uint32 IDs, so
// comparisons in the hot matching path become integer equality instead of
// string comparison, and a k=v pair packs into a single uint64 (see kv).
type symbolTable struct {
	ids    map[string]uint32
	values []string
}

func newSymbolTable() *symbolTable {
	return &symbolTable{ids: map[string]uint32{}}
}

func (t *symbolTable) intern(s string) uint32 {
	if id, ok := t.ids[s]; ok {
		return id
	}
	id := uint32(len(t.values))
	t.ids[s] = id
	t.values = append(t.values, s)
	return id
}

func kv(key, value uint32) uint64 {
	return uint64(key)<<32 | uint64(value)
}

type residualOp int

const (
	residualDoesNotExist residualOp = iota
	residualIn                      // multi-value In
	residualNotIn
)

// residualReq is a selector requirement that can't be answered by presence
// in an inverted index (see compiledSelector) and must be evaluated directly
// against a candidate label set.
type residualReq struct {
	key    uint32
	op     residualOp
	values map[uint32]struct{}
}

// compiledSelector is a metav1.LabelSelector compiled once into interned
// integer form, split per doc/optimizations.md "Label optimizations" #2:
//
//   - exact: k=v requirements (MatchLabels, and single-value In) that are
//     indexable via a simple inverted index.
//   - existsKeys: Exists requirements, also indexable (by key alone, #3).
//   - residual: DoesNotExist/NotIn/multi-value-In, which are rare and only
//     evaluated on candidates that already cleared the indexed part.
type compiledSelector struct {
	exact      []uint64 // sorted
	existsKeys []uint32 // sorted
	residual   []residualReq
}

// positiveCount is the number of indexable positive requirements (exact +
// Exists) a matching label set must satisfy.
func (cs compiledSelector) positiveCount() int {
	return len(cs.exact) + len(cs.existsKeys)
}

// compileSelector converts sel into compiled form, interning every key/value
// it references into tbl.
func compileSelector(sel metav1.LabelSelector, tbl *symbolTable) (compiledSelector, error) {
	var cs compiledSelector

	for k, v := range sel.MatchLabels {
		cs.exact = append(cs.exact, kv(tbl.intern(k), tbl.intern(v)))
	}

	for _, expr := range sel.MatchExpressions {
		key := tbl.intern(expr.Key)
		switch expr.Operator {
		case metav1.LabelSelectorOpIn:
			if len(expr.Values) == 0 {
				return compiledSelector{}, fmt.Errorf("selector: In requires at least one value for key %q", expr.Key)
			}
			if len(expr.Values) == 1 {
				cs.exact = append(cs.exact, kv(key, tbl.intern(expr.Values[0])))
				continue
			}
			values := make(map[uint32]struct{}, len(expr.Values))
			for _, v := range expr.Values {
				values[tbl.intern(v)] = struct{}{}
			}
			cs.residual = append(cs.residual, residualReq{key: key, op: residualIn, values: values})
		case metav1.LabelSelectorOpNotIn:
			if len(expr.Values) == 0 {
				return compiledSelector{}, fmt.Errorf("selector: NotIn requires at least one value for key %q", expr.Key)
			}
			values := make(map[uint32]struct{}, len(expr.Values))
			for _, v := range expr.Values {
				values[tbl.intern(v)] = struct{}{}
			}
			cs.residual = append(cs.residual, residualReq{key: key, op: residualNotIn, values: values})
		case metav1.LabelSelectorOpExists:
			cs.existsKeys = append(cs.existsKeys, key)
		case metav1.LabelSelectorOpDoesNotExist:
			cs.residual = append(cs.residual, residualReq{key: key, op: residualDoesNotExist})
		default:
			return compiledSelector{}, fmt.Errorf("selector: unsupported operator %q", expr.Operator)
		}
	}

	sort.Slice(cs.exact, func(i, j int) bool { return cs.exact[i] < cs.exact[j] })
	sort.Slice(cs.existsKeys, func(i, j int) bool { return cs.existsKeys[i] < cs.existsKeys[j] })
	return cs, nil
}

// matches is the full (exact + Exists + residual) evaluation of cs against
// ls. It's the ground-truth semantics; the inverted index in index.go is an
// optimization of exactly this check at scale, and namespaceSelectorCache
// uses this directly since the namespace set is always small.
func (cs compiledSelector) matches(ls internedLabelSet) bool {
	for _, e := range cs.exact {
		key, val := uint32(e>>32), uint32(e)
		if v, ok := ls.kv[key]; !ok || v != val {
			return false
		}
	}
	for _, k := range cs.existsKeys {
		if _, ok := ls.kv[k]; !ok {
			return false
		}
	}
	return cs.matchesResidual(ls)
}

func (cs compiledSelector) matchesResidual(ls internedLabelSet) bool {
	for _, r := range cs.residual {
		v, present := ls.kv[r.key]
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

// canonicalKey produces a string that's identical for two metav1.LabelSelector
// values with the same meaning regardless of map/slice iteration order, so
// selectorRegistry and namespaceSelectorCache can dedupe repeated identical
// selectors (common in practice: many rules share the same peer selector).
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
