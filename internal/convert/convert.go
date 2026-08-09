package convert

import (
	"fmt"
	"maps"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/kvec/pkg/pred"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
)

// LabelsToKV converts a Kubernetes label map into the matching-optimized form
// repr stores it in. The result is immutable and safe to copy by value and to
// match against from many goroutines.
func LabelsToKV(labels map[string]string) kv.KV {
	return *kv.NewKV(maps.All(labels), len(labels))
}

// RelevantLabels compiles the pairs of labels whose key is in keys into the
// form matching runs against, dropping the rest.
//
// keys is the set of label keys some policy selects on
// (store.NetworkPolicies.RelevantKeys). A key outside it cannot change whether
// any selector matches, so dropping it costs nothing and buys the equivalence
// classes their coarseness: objects differing only in labels no policy names —
// pod-template-hash, chart versions, the bookkeeping that dominates a real
// object's label set — collapse into one class and are resolved once.
//
// The result is immutable and safe to match against from many goroutines. It is
// returned by pointer because that is what eqc holds onto: every object in a
// class shares the one projection rather than a copy of it.
func RelevantLabels(labels map[string]string, keys set.Set[string]) *kv.KV {
	// The projection can be built by walking either side, and neither is
	// reliably the smaller: an object carries tens of labels, while the relevant
	// set is every key every policy in the cluster names, which can be anywhere
	// from none to thousands. Walking the smaller one keeps the lookups
	// proportional to it. This runs once per object per resync and again for
	// every object when the relevant set changes, so the difference is real.
	n := min(len(labels), len(keys))
	return kv.NewKV(func(yield func(k, v string) bool) {
		if len(labels) <= len(keys) {
			for k, v := range labels {
				if keys.Has(k) && !yield(k, v) {
					return
				}
			}
			return
		}
		for k := range keys {
			if v, ok := labels[k]; ok && !yield(k, v) {
				return
			}
		}
	}, n)
}

// SelectorToPred compiles sel into the matching-optimized form repr stores
// selectors in. The result is immutable and safe to match against from many
// goroutines.
//
// A nil sel yields a nil Predicate. In the NetworkPolicy API an omitted
// selector ("not specified", e.g. a peer's namespaceSelector) is not the same
// as an empty one ("match everything"), and only the caller knows what the
// omission means for the field it is reading, so the distinction is preserved
// rather than resolved here.
func SelectorToPred(sel *metav1.LabelSelector) (*pred.Predicate, error) {
	if sel == nil {
		return nil, nil
	}
	// matchLabels and matchExpressions are ANDed together, which is exactly
	// what a Predicate is: a conjunction of terms. An empty selector builds a
	// Predicate with no terms, which matches everything.
	b := pred.NewPredicateBuilder()
	if len(sel.MatchLabels) > 0 {
		b.Has(sel.MatchLabels)
	}
	for _, e := range sel.MatchExpressions {
		switch e.Operator {
		case metav1.LabelSelectorOpIn:
			// An empty value set is rejected by API validation; pred treats it
			// as unsatisfiable, which is the same answer.
			b.ValueIn(e.Key, e.Values)
		case metav1.LabelSelectorOpNotIn:
			b.ValueNotIn(e.Key, e.Values)
		case metav1.LabelSelectorOpExists:
			b.Key(e.Key)
		case metav1.LabelSelectorOpDoesNotExist:
			b.NoKey(e.Key)
		default:
			return nil, fmt.Errorf("unsupported label selector operator %q on key %q", e.Operator, e.Key)
		}
	}
	return b.Build(), nil
}
