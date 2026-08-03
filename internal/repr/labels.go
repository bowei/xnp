package repr

import (
	"fmt"
	"maps"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/kvec/pkg/pred"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// labelsToKV converts a Kubernetes label map into the matching-optimized form
// repr stores it in. The result is immutable and safe to copy by value and to
// match against from many goroutines.
func labelsToKV(labels map[string]string) kv.KV {
	return *kv.New(maps.All(labels), len(labels))
}

// selectorToPred compiles sel into the matching-optimized form repr stores
// selectors in. The result is immutable and safe to match against from many
// goroutines.
//
// A nil sel yields a nil Predicate. In the NetworkPolicy API an omitted
// selector ("not specified", e.g. a peer's namespaceSelector) is not the same
// as an empty one ("match everything"), and only the caller knows what the
// omission means for the field it is reading, so the distinction is preserved
// rather than resolved here.
func selectorToPred(sel *metav1.LabelSelector) (*pred.Predicate, error) {
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
