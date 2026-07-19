// Package testutil holds fixture builders and assertion helpers shared by
// policy/basic's and policy/indexed's tests, so hand-written NetworkPolicy
// fixtures and tuple-comparison logic aren't duplicated across packages. It
// mirrors the stdlib httptest/iotest convention of a test-support package
// separate from the production code it exercises.
package testutil

import (
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/policy/common"
	"github.com/bowei/xnp/internal/tuple"
)

func Pod(namespace, name, ip string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

func Namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// MustInput runs Phase 1 (common.NewInput) for a test fixture, failing the
// test on conversion error so every call site doesn't have to.
func MustInput(t *testing.T, policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace) *common.Input {
	t.Helper()
	in, err := common.NewInput(policies, pods, namespaces)
	if err != nil {
		t.Fatalf("NewInput: %v", err)
	}
	return in
}

// SortTuples gives tests a stable order to compare against, independent of
// the (documented but not load-bearing) emission order.
func SortTuples(tuples []tuple.Tuple) []tuple.Tuple {
	out := append([]tuple.Tuple(nil), tuples...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pod != b.Pod {
			return a.Pod.String() < b.Pod.String()
		}
		if a.Direction != b.Direction {
			return a.Direction < b.Direction
		}
		if a.Src != b.Src {
			return a.Src < b.Src
		}
		if a.Dest != b.Dest {
			return a.Dest < b.Dest
		}
		return a.Verdict < b.Verdict
	})
	return out
}

func NSName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

func AssertTuples(t *testing.T, got, want []tuple.Tuple) {
	t.Helper()
	gotSorted, wantSorted := SortTuples(got), SortTuples(want)
	if len(gotSorted) != len(wantSorted) {
		t.Fatalf("tuple count mismatch: got %d, want %d\ngot:  %+v\nwant: %+v", len(gotSorted), len(wantSorted), gotSorted, wantSorted)
	}
	for i := range gotSorted {
		g, w := gotSorted[i], wantSorted[i]
		if g.Pod != w.Pod || g.Src != w.Src || g.Dest != w.Dest || g.Direction != w.Direction || g.Verdict != w.Verdict || g.Protocol != w.Protocol || !int32PtrEqual(g.Port, w.Port) || !int32PtrEqual(g.EndPort, w.EndPort) {
			t.Fatalf("tuple %d mismatch:\ngot:  %+v\nwant: %+v", i, g, w)
		}
	}
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
