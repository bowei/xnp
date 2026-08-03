package eqc

import (
	"maps"
	"slices"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
)

func labels(pairs map[string]string) *kv.KV {
	return kv.New(func(yield func(k, v string) bool) {
		for k, v := range pairs {
			if !yield(k, v) {
				return
			}
		}
	}, len(pairs))
}

// classNames is the sorted object names of the Class holding (ns, name), or nil
// if the object is not tracked.
func classNames(t *testing.T, cl *Classes, ns, name string) []string {
	t.Helper()
	cls, ok := cl.Get(ns, name)
	if !ok {
		return nil
	}
	return slices.Sorted(cls.Objects())
}

func countClasses(cl *Classes) int {
	var n int
	for range cl.All() {
		n++
	}
	return n
}

func TestAddGroupsEqualLabels(t *testing.T) {
	web := map[string]string{"app": "web", "tier": "front"}
	db := map[string]string{"app": "db"}

	var cl Classes
	cl.Add("ns1", "web-1", labels(web))
	// Same content, different *KV and different insertion order: identity is
	// the key/value set, not the pointer.
	cl.Add("ns1", "web-2", labels(maps.Clone(web)))
	cl.Add("ns1", "db-1", labels(db))
	// Same labels in another namespace: never shares a class across namespaces.
	cl.Add("ns2", "web-3", labels(web))

	if got, want := countClasses(&cl), 3; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}
	if got, want := classNames(t, &cl, "ns1", "web-1"), []string{"web-1", "web-2"}; !slices.Equal(got, want) {
		t.Errorf("ns1/web-1 class objects = %v, want %v", got, want)
	}
	if got, want := classNames(t, &cl, "ns1", "db-1"), []string{"db-1"}; !slices.Equal(got, want) {
		t.Errorf("ns1/db-1 class objects = %v, want %v", got, want)
	}
	if got, want := classNames(t, &cl, "ns2", "web-3"), []string{"web-3"}; !slices.Equal(got, want) {
		t.Errorf("ns2/web-3 class objects = %v, want %v", got, want)
	}

	cls, ok := cl.Get("ns1", "web-1")
	if !ok {
		t.Fatalf("Get(ns1, web-1) ok = false, want true")
	}
	if cls.Namespace() != "ns1" {
		t.Errorf("Namespace() = %q, want %q", cls.Namespace(), "ns1")
	}
	if cls.Len() != 2 {
		t.Errorf("Len() = %d, want 2", cls.Len())
	}
	if !cls.Has("web-2") || cls.Has("db-1") {
		t.Errorf("Has(web-2) = %v, Has(db-1) = %v, want true, false", cls.Has("web-2"), cls.Has("db-1"))
	}
	if !cls.KV().Equal(labels(web)) {
		t.Errorf("KV() does not equal the labels the class was created from")
	}
}

func TestGetUnknown(t *testing.T) {
	var cl Classes
	cl.Add("ns1", "web-1", labels(map[string]string{"app": "web"}))

	if cls, ok := cl.Get("ns1", "nope"); ok || cls != nil {
		t.Errorf("Get(ns1, nope) = %v, %v, want nil, false", cls, ok)
	}
	if cls, ok := cl.Get("nope", "web-1"); ok || cls != nil {
		t.Errorf("Get(nope, web-1) = %v, %v, want nil, false", cls, ok)
	}
}

func TestUpdate(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db"}

	var cl Classes
	cl.Add("ns1", "pod-1", labels(web))
	cl.Add("ns1", "pod-2", labels(web))

	// Unchanged labels: the object stays where it is and no class is created.
	before, _ := cl.Get("ns1", "pod-1")
	cl.Update("ns1", "pod-1", labels(web))
	after, _ := cl.Get("ns1", "pod-1")
	if before != after {
		t.Errorf("Update with equal labels moved pod-1 to a different class")
	}
	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}

	// Changed labels: moves to a new class, leaving the old one behind.
	cl.Update("ns1", "pod-1", labels(db))
	if got, want := classNames(t, &cl, "ns1", "pod-1"), []string{"pod-1"}; !slices.Equal(got, want) {
		t.Errorf("pod-1 class objects = %v, want %v", got, want)
	}
	if got, want := classNames(t, &cl, "ns1", "pod-2"), []string{"pod-2"}; !slices.Equal(got, want) {
		t.Errorf("pod-2 class objects = %v, want %v", got, want)
	}
	if got, want := countClasses(&cl), 2; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}

	// The last object out of a class takes the class with it.
	cl.Update("ns1", "pod-2", labels(db))
	if got, want := classNames(t, &cl, "ns1", "pod-2"), []string{"pod-1", "pod-2"}; !slices.Equal(got, want) {
		t.Errorf("pod-2 class objects = %v, want %v", got, want)
	}
	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}
}

func TestUpdateUntrackedAdds(t *testing.T) {
	var cl Classes
	cl.Update("ns1", "pod-1", labels(map[string]string{"app": "web"}))

	if got, want := classNames(t, &cl, "ns1", "pod-1"), []string{"pod-1"}; !slices.Equal(got, want) {
		t.Errorf("pod-1 class objects = %v, want %v", got, want)
	}
}

func TestAddExistingBehavesAsUpdate(t *testing.T) {
	var cl Classes
	cl.Add("ns1", "pod-1", labels(map[string]string{"app": "web"}))
	cl.Add("ns1", "pod-1", labels(map[string]string{"app": "db"}))

	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d (the old class should not be left behind)", got, want)
	}
	if got, want := classNames(t, &cl, "ns1", "pod-1"), []string{"pod-1"}; !slices.Equal(got, want) {
		t.Errorf("pod-1 class objects = %v, want %v", got, want)
	}
	cls, _ := cl.Get("ns1", "pod-1")
	if !cls.KV().Equal(labels(map[string]string{"app": "db"})) {
		t.Errorf("pod-1 kept its old labels")
	}
}

func TestDelete(t *testing.T) {
	web := map[string]string{"app": "web"}

	var cl Classes
	cl.Add("ns1", "pod-1", labels(web))
	cl.Add("ns1", "pod-2", labels(web))
	cl.Add("ns2", "pod-3", labels(web))

	cl.Delete("ns1", "pod-1")
	if _, ok := cl.Get("ns1", "pod-1"); ok {
		t.Errorf("Get(ns1, pod-1) ok = true after Delete, want false")
	}
	if got, want := classNames(t, &cl, "ns1", "pod-2"), []string{"pod-2"}; !slices.Equal(got, want) {
		t.Errorf("pod-2 class objects = %v, want %v", got, want)
	}

	// Emptying a namespace drops it entirely, and does not disturb the others.
	cl.Delete("ns1", "pod-2")
	if _, ok := cl.byNs["ns1"]; ok {
		t.Errorf("ns1 still tracked after its last object was deleted")
	}
	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}
	if got, want := classNames(t, &cl, "ns2", "pod-3"), []string{"pod-3"}; !slices.Equal(got, want) {
		t.Errorf("pod-3 class objects = %v, want %v", got, want)
	}

	// Unknown objects and namespaces are no-ops.
	cl.Delete("ns1", "pod-1")
	cl.Delete("ns2", "nope")
	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}
}

func TestDeleteOnZeroValue(t *testing.T) {
	var cl Classes
	cl.Delete("ns1", "pod-1")
	if _, ok := cl.Get("ns1", "pod-1"); ok {
		t.Errorf("Get on empty Classes ok = true, want false")
	}
}

func TestRange(t *testing.T) {
	var cl Classes
	cl.Add("ns1", "web-1", labels(map[string]string{"app": "web"}))
	cl.Add("ns1", "db-1", labels(map[string]string{"app": "db"}))
	cl.Add("ns2", "web-2", labels(map[string]string{"app": "web"}))

	var ns1 []string
	for cls := range cl.Range("ns1") {
		if cls.Namespace() != "ns1" {
			t.Errorf("Range(ns1) yielded a class in %q", cls.Namespace())
		}
		ns1 = append(ns1, slices.Sorted(cls.Objects())...)
	}
	slices.Sort(ns1)
	if want := []string{"db-1", "web-1"}; !slices.Equal(ns1, want) {
		t.Errorf("Range(ns1) objects = %v, want %v", ns1, want)
	}

	for range cl.Range("nope") {
		t.Errorf("Range(nope) yielded a class")
	}

	// Both iterators stop when the caller breaks out.
	var n int
	for range cl.All() {
		n++
		break
	}
	if n != 1 {
		t.Errorf("All() yielded %d classes after break, want 1", n)
	}
}

// A class holding one object must not be reachable through a name that has
// moved out of it, even when the object it left behind keeps the class alive.
func TestEvictKeepsSiblings(t *testing.T) {
	web := map[string]string{"app": "web"}

	var cl Classes
	for _, name := range []string{"pod-1", "pod-2", "pod-3"} {
		cl.Add("ns1", name, labels(web))
	}
	cl.Update("ns1", "pod-2", labels(map[string]string{"app": "db"}))

	if got, want := classNames(t, &cl, "ns1", "pod-1"), []string{"pod-1", "pod-3"}; !slices.Equal(got, want) {
		t.Errorf("pod-1 class objects = %v, want %v", got, want)
	}
	if cls, _ := cl.Get("ns1", "pod-1"); cls.Has("pod-2") {
		t.Errorf("pod-2 is still in the class it was evicted from")
	}
}
