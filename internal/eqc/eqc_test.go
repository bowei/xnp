package eqc

import (
	"maps"
	"slices"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
)

func labels(pairs map[string]string) *kv.KV {
	return kv.NewKV(func(yield func(k, v string) bool) {
		for k, v := range pairs {
			if !yield(k, v) {
				return
			}
		}
	}, len(pairs))
}

// entry is the Entry for an object in ns carrying pairs, grouped by all of
// them. Tests about HasKey want a projection narrower than the object's labels
// and build their Entry directly.
func entry(ns, name string, pairs map[string]string) Entry {
	return Entry{
		Namespace: ns,
		Name:      name,
		KV:        labels(pairs),
		AllKeys:   slices.Collect(maps.Keys(pairs)),
	}
}

// projected is the Entry for an object carrying pairs but grouped only by the
// pairs whose key is in relevant, which is what the Pod store does: the class
// is the projection onto the keys some policy selects on, while AllKeys stays
// the object's full set.
func projected(ns, name string, pairs map[string]string, relevant ...string) Entry {
	kept := map[string]string{}
	for _, k := range relevant {
		if v, ok := pairs[k]; ok {
			kept[k] = v
		}
	}
	e := entry(ns, name, pairs)
	e.KV = labels(kept)
	return e
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
	cl.Add(entry("ns1", "web-1", web))
	// Same content, different *KV and different insertion order: identity is
	// the key/value set, not the pointer.
	cl.Add(entry("ns1", "web-2", maps.Clone(web)))
	cl.Add(entry("ns1", "db-1", db))
	// Same labels in another namespace: never shares a class across namespaces.
	cl.Add(entry("ns2", "web-3", web))

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
	cl.Add(entry("ns1", "web-1", map[string]string{"app": "web"}))

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
	cl.Add(entry("ns1", "pod-1", web))
	cl.Add(entry("ns1", "pod-2", web))

	// Unchanged labels: the object stays where it is and no class is created.
	before, _ := cl.Get("ns1", "pod-1")
	cl.Update(entry("ns1", "pod-1", web))
	after, _ := cl.Get("ns1", "pod-1")
	if before != after {
		t.Errorf("Update with equal labels moved pod-1 to a different class")
	}
	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}

	// Changed labels: moves to a new class, leaving the old one behind.
	cl.Update(entry("ns1", "pod-1", db))
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
	cl.Update(entry("ns1", "pod-2", db))
	if got, want := classNames(t, &cl, "ns1", "pod-2"), []string{"pod-1", "pod-2"}; !slices.Equal(got, want) {
		t.Errorf("pod-2 class objects = %v, want %v", got, want)
	}
	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}
}

func TestUpdateUntrackedAdds(t *testing.T) {
	var cl Classes
	cl.Update(entry("ns1", "pod-1", map[string]string{"app": "web"}))

	if got, want := classNames(t, &cl, "ns1", "pod-1"), []string{"pod-1"}; !slices.Equal(got, want) {
		t.Errorf("pod-1 class objects = %v, want %v", got, want)
	}
}

func TestAddExistingBehavesAsUpdate(t *testing.T) {
	var cl Classes
	cl.Add(entry("ns1", "pod-1", map[string]string{"app": "web"}))
	cl.Add(entry("ns1", "pod-1", map[string]string{"app": "db"}))

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
	cl.Add(entry("ns1", "pod-1", web))
	cl.Add(entry("ns1", "pod-2", web))
	cl.Add(entry("ns2", "pod-3", web))

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
	cl.Add(entry("ns1", "web-1", map[string]string{"app": "web"}))
	cl.Add(entry("ns1", "db-1", map[string]string{"app": "db"}))
	cl.Add(entry("ns2", "web-2", map[string]string{"app": "web"}))

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

// hasKeys is the answer HasKey gives for each of keys, for the class holding
// (ns, name).
func hasKeys(t *testing.T, cl *Classes, ns, name string, keys ...string) []bool {
	t.Helper()
	cls, ok := cl.Get(ns, name)
	if !ok {
		t.Fatalf("Get(%s, %s) ok = false, want true", ns, name)
	}
	got := make([]bool, len(keys))
	for i, k := range keys {
		got[i] = cls.HasKey(k)
	}
	return got
}

func TestHasKey(t *testing.T) {
	// Two objects that share a class because they agree on the one relevant
	// key, while carrying different keys outside it.
	var cl Classes
	cl.Add(projected("ns1", "pod-1", map[string]string{"app": "web", "rev": "1"}, "app"))
	cl.Add(projected("ns1", "pod-2", map[string]string{"app": "web", "team": "net"}, "app"))

	if got, want := countClasses(&cl), 1; got != want {
		t.Errorf("classes = %d, want %d", got, want)
	}
	got := hasKeys(t, &cl, "ns1", "pod-1", "app", "rev", "team", "nope")
	if want := []bool{true, true, true, false}; !slices.Equal(got, want) {
		t.Errorf("HasKey(app, rev, team, nope) = %v, want %v", got, want)
	}

	// A key another object in the class still carries survives an eviction; one
	// only the evicted object carried goes with it.
	cl.Delete("ns1", "pod-2")
	got = hasKeys(t, &cl, "ns1", "pod-1", "app", "rev", "team")
	if want := []bool{true, true, false}; !slices.Equal(got, want) {
		t.Errorf("HasKey(app, rev, team) after deleting pod-2 = %v, want %v", got, want)
	}
}

// A label on a key nothing selects on leaves the object in the class it is
// already in, so the filter has to be maintained on that path too — it is the
// only one that sees the change at all.
func TestHasKeyLabelOutsideProjection(t *testing.T) {
	var cl Classes
	cl.Add(projected("ns1", "pod-1", map[string]string{"app": "web", "rev": "1"}, "app"))

	before, _ := cl.Get("ns1", "pod-1")
	cl.Update(projected("ns1", "pod-1", map[string]string{"app": "web", "rev": "2", "team": "net"}, "app"))
	after, _ := cl.Get("ns1", "pod-1")
	if before != after {
		t.Errorf("a label outside the projection moved pod-1 to a different class")
	}
	got := hasKeys(t, &cl, "ns1", "pod-1", "app", "rev", "team")
	if want := []bool{true, true, true}; !slices.Equal(got, want) {
		t.Errorf("HasKey(app, rev, team) = %v, want %v", got, want)
	}

	// Dropping a key retracts it, and the keys that stayed are not disturbed by
	// the replay that does it.
	cl.Update(projected("ns1", "pod-1", map[string]string{"app": "web"}, "app"))
	got = hasKeys(t, &cl, "ns1", "pod-1", "app", "rev", "team")
	if want := []bool{true, false, false}; !slices.Equal(got, want) {
		t.Errorf("HasKey(app, rev, team) after dropping them = %v, want %v", got, want)
	}
}

// An object moving between classes is added to the filter of the class it
// lands in and taken out of the one it left, so neither ends up answering for
// the other's keys.
func TestHasKeyMoveBetweenClasses(t *testing.T) {
	var cl Classes
	cl.Add(projected("ns1", "pod-1", map[string]string{"app": "web", "rev": "1"}, "app"))
	cl.Add(projected("ns1", "pod-2", map[string]string{"app": "web", "rev": "1"}, "app"))
	cl.Update(projected("ns1", "pod-1", map[string]string{"app": "db", "shard": "0"}, "app"))

	// The class pod-1 landed in knows only the keys pod-1 arrived with.
	got := hasKeys(t, &cl, "ns1", "pod-1", "app", "shard", "rev")
	if want := []bool{true, true, false}; !slices.Equal(got, want) {
		t.Errorf("moved class HasKey(app, shard, rev) = %v, want %v", got, want)
	}
	// The class it left keeps pod-2's keys and gains nothing from the move.
	got = hasKeys(t, &cl, "ns1", "pod-2", "app", "shard", "rev")
	if want := []bool{true, false, true}; !slices.Equal(got, want) {
		t.Errorf("old class HasKey(app, shard, rev) = %v, want %v", got, want)
	}
}

// An Entry with no keys is allowed: nothing is added to the filter, and the
// object still groups by its labels.
func TestHasKeyNoKeys(t *testing.T) {
	var cl Classes
	cls := cl.Add(Entry{Namespace: "ns1", Name: "pod-1", KV: labels(map[string]string{"app": "web"})})
	if cls.HasKey("app") {
		t.Errorf("HasKey(app) = true for an Entry with no AllKeys, want false")
	}
}

// A class holding one object must not be reachable through a name that has
// moved out of it, even when the object it left behind keeps the class alive.
func TestEvictKeepsSiblings(t *testing.T) {
	web := map[string]string{"app": "web"}

	var cl Classes
	for _, name := range []string{"pod-1", "pod-2", "pod-3"} {
		cl.Add(entry("ns1", name, web))
	}
	cl.Update(entry("ns1", "pod-2", map[string]string{"app": "db"}))

	if got, want := classNames(t, &cl, "ns1", "pod-1"), []string{"pod-1", "pod-3"}; !slices.Equal(got, want) {
		t.Errorf("pod-1 class objects = %v, want %v", got, want)
	}
	if cls, _ := cl.Get("ns1", "pod-1"); cls.Has("pod-2") {
		t.Errorf("pod-2 is still in the class it was evicted from")
	}
}
