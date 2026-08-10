package pods

import (
	"slices"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/convert"
	"github.com/bowei/xnp/internal/rlabels"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
)

func testPod(ns, name, node string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: node},
	}
}

func podSeq(pods ...*corev1.Pod) func(yield func(*corev1.Pod) bool) {
	return func(yield func(*corev1.Pod) bool) {
		for _, p := range pods {
			if !yield(p) {
				return
			}
		}
	}
}

// classKey is the equivalence class labels projects onto under keys, in the
// form the eqc index is keyed by. The test builds its expectations the same way
// the store does rather than reaching for the entries' own EQC, so a wrong
// projection shows up as a missing class and not as agreement with itself.
func classKey(labels map[string]string, keys ...string) kv.MapKey {
	return convert.RelevantLabels(labels, set.New(keys...)).Comparable()
}

// testRLabels builds the relevant labels of a cluster from ns -> keys, the
// shape the policy store hands them over in. A namespace with no keys is left
// out, as the Builder leaves it out.
func testRLabels(perNs map[string][]string) *rlabels.RLabels {
	var b rlabels.Builder
	for ns, keys := range perNs {
		b.Merge(ns, set.New(keys...))
	}
	return b.Build()
}

// classes is the eqc index of ns flattened to sorted member names per class.
func classes(t *testing.T, pods *Store, ns string) map[kv.MapKey][]string {
	t.Helper()
	got := map[kv.MapKey][]string{}
	for key, members := range pods.eqcToPod[ns] {
		if len(members) == 0 {
			t.Errorf("eqc[%q][%q] is empty, want drained classes to be pruned", ns, key)
		}
		names := make([]string, 0, len(members))
		for name, e := range members {
			names = append(names, name)
			if e.EQC.Comparable() != key {
				t.Errorf("eqc[%q][%q][%q].EQC = %q, want it to agree with its bucket", ns, key, name, e.EQC.Comparable())
			}
		}
		slices.Sort(names)
		got[key] = names
	}

	// The class list is checked here rather than in a test of its own because
	// it is not an independent fact: it has to name exactly the live classes of
	// the namespace, so every test that asserts a grouping asserts this too.
	classList := pods.eqc[ns]
	if got, want := len(classList), len(got); got != want {
		t.Errorf("len(eqc classes[%q]) = %v, want %v", ns, got, want)
	}
	for key, class := range classList {
		if _, ok := pods.eqcToPod[ns][key]; !ok {
			t.Errorf("eqc classes[%q] holds %q, want only classes with members", ns, key)
		}
		if class.Comparable() != key {
			t.Errorf("eqc classes[%q][%q] = %q, want it to agree with its key", ns, key, class.Comparable())
		}
	}

	return got
}

func nodeClasses(pods *Store) map[string][]kv.MapKey {
	got := map[string][]kv.MapKey{}
	for node, keys := range pods.nodes {
		got[node] = keys.SortedList()
	}
	return got
}

var (
	webFE = map[string]string{"app": "web", "tier": "fe"}
	webBE = map[string]string{"app": "web", "tier": "be"}
	db    = map[string]string{"app": "db"}
	// unlabeled shares no key with any relevant set the tests use, so it must
	// sit in the empty class and stay there through every re-grouping.
	unlabeled = map[string]string{"unrelated": "x"}
)

func initPods(t *testing.T, perNs map[string][]string) *Store {
	t.Helper()
	pods := New()
	err := pods.Init(testRLabels(perNs), podSeq(
		testPod("ns1", "web-1", "node-a", webFE),
		testPod("ns1", "web-2", "node-a", webBE),
		testPod("ns1", "db-1", "node-b", db),
		testPod("ns2", "idle-1", "node-b", unlabeled),
	))
	if err != nil {
		t.Fatalf("Init() = %v, want nil", err)
	}
	return pods
}

func TestUpdateRLabelsSplitsClasses(t *testing.T) {
	pods := initPods(t, map[string][]string{"ns1": {"app"}})

	// "app" alone cannot tell the two web pods apart.
	want := map[kv.MapKey][]string{
		classKey(webFE, "app"): {"web-1", "web-2"},
		classKey(db, "app"):    {"db-1"},
	}
	if got := classes(t, pods, "ns1"); !mapsEqual(got, want) {
		t.Errorf("classes(ns1) = %v, want %v", got, want)
	}

	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app", "tier"}}))

	// "tier" becoming relevant splits them, and leaves db-1 — which carries no
	// tier — in the class it was already in.
	want = map[kv.MapKey][]string{
		classKey(webFE, "app", "tier"): {"web-1"},
		classKey(webBE, "app", "tier"): {"web-2"},
		classKey(db, "app", "tier"):    {"db-1"},
	}
	if got := classes(t, pods, "ns1"); !mapsEqual(got, want) {
		t.Errorf("classes(ns1) after split = %v, want %v", got, want)
	}
	if got, want := classKey(db, "app"), classKey(db, "app", "tier"); got != want {
		t.Errorf("db class moved on an update that cannot touch it: %q vs %q", got, want)
	}

	// The pods themselves are untouched by a re-grouping.
	if got, want := len(pods.pods["ns1"]), 3; got != want {
		t.Errorf("len(pods[ns1]) = %v, want %v", got, want)
	}
}

func TestUpdateRLabelsMergesClasses(t *testing.T) {
	pods := initPods(t, map[string][]string{"ns1": {"app", "tier"}})
	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app"}}))

	// Dropping "tier" collapses the two web classes back into one, and the
	// class each pod left must not be lingering empty behind it. classes()
	// checks the pruning.
	want := map[kv.MapKey][]string{
		classKey(webFE, "app"): {"web-1", "web-2"},
		classKey(db, "app"):    {"db-1"},
	}
	if got := classes(t, pods, "ns1"); !mapsEqual(got, want) {
		t.Errorf("classes(ns1) = %v, want %v", got, want)
	}
}

// Relevant labels are per namespace, so a key becoming relevant in one
// namespace says nothing about the pods of another, even when they carry the
// very same labels.
func TestUpdateRLabelsScopedToNamespace(t *testing.T) {
	pods := New()
	if err := pods.Init(testRLabels(map[string][]string{"ns1": {"app"}}), podSeq(
		testPod("ns1", "web-1", "node-a", webFE),
		testPod("ns2", "web-2", "node-a", webFE),
	)); err != nil {
		t.Fatalf("Init() = %v, want nil", err)
	}

	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app", "tier"}}))

	if got, want := classes(t, pods, "ns1"), map[kv.MapKey][]string{
		classKey(webFE, "app", "tier"): {"web-1"},
	}; !mapsEqual(got, want) {
		t.Errorf("classes(ns1) = %v, want %v", got, want)
	}
	// No policy names a key in ns2, so its pod is still in the empty class
	// rather than in the one the identically labeled ns1 pod moved to.
	if got, want := classes(t, pods, "ns2"), map[kv.MapKey][]string{
		classKey(webFE): {"web-2"},
	}; !mapsEqual(got, want) {
		t.Errorf("classes(ns2) = %v, want %v", got, want)
	}

	pods.UpdateRLabels(testRLabels(map[string][]string{
		"ns1": {"app", "tier"},
		"ns2": {"app"},
	}))

	// ns2 gaining its first relevant key regroups it, and leaves ns1 — which
	// the update does not name a different set for — where it was.
	if got, want := classes(t, pods, "ns2"), map[kv.MapKey][]string{
		classKey(webFE, "app"): {"web-2"},
	}; !mapsEqual(got, want) {
		t.Errorf("classes(ns2) after ns2 becomes relevant = %v, want %v", got, want)
	}
	if got, want := classes(t, pods, "ns1"), map[kv.MapKey][]string{
		classKey(webFE, "app", "tier"): {"web-1"},
	}; !mapsEqual(got, want) {
		t.Errorf("classes(ns1) after ns2 becomes relevant = %v, want %v", got, want)
	}
	// Both classes live on node-a, and they are distinct: the node index is not
	// namespace qualified, so the two projections of the same labels are two
	// entries there.
	if got, want := nodeClasses(pods), map[string][]kv.MapKey{
		"node-a": sorted(classKey(webFE, "app", "tier"), classKey(webFE, "app")),
	}; !mapsEqual(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
}

func TestUpdateRLabelsEmptyRelevantSet(t *testing.T) {
	pods := initPods(t, map[string][]string{"ns1": {"app", "tier"}})
	pods.UpdateRLabels(testRLabels(nil))

	// With nothing relevant every pod in a namespace projects onto the empty
	// set, so a namespace is one class.
	want := map[kv.MapKey][]string{
		classKey(nil): {"db-1", "web-1", "web-2"},
	}
	if got := classes(t, pods, "ns1"); !mapsEqual(got, want) {
		t.Errorf("classes(ns1) = %v, want %v", got, want)
	}
	if got, want := len(pods.eqcToPod["ns2"]), 1; got != want {
		t.Errorf("len(eqc[ns2]) = %v, want %v", got, want)
	}
}

func TestUpdateRLabelsNoChange(t *testing.T) {
	pods := initPods(t, map[string][]string{"ns1": {"app"}})
	before := classes(t, pods, "ns1")

	// A key set that differs by an irrelevant key, then one that does not
	// differ at all: neither may move a pod.
	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app", "nobody-carries-this"}}))
	if got := classes(t, pods, "ns1"); !mapsEqual(got, before) {
		t.Errorf("classes(ns1) after unused key = %v, want %v", got, before)
	}
	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app", "nobody-carries-this"}}))
	if got := classes(t, pods, "ns1"); !mapsEqual(got, before) {
		t.Errorf("classes(ns1) after no-op = %v, want %v", got, before)
	}
}

func TestUpdateRLabelsRebuildsNodes(t *testing.T) {
	pods := initPods(t, map[string][]string{"ns1": {"app"}})

	// node-b runs pods from both namespaces: db-1 from ns1 and idle-1 from ns2,
	// which has no relevant keys at all and so sits in the empty class.
	want := map[string][]kv.MapKey{
		"node-a": {classKey(webFE, "app")},
		"node-b": sorted(classKey(db, "app"), classKey(unlabeled)),
	}
	if got := nodeClasses(pods); !mapsEqual(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}

	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app", "tier"}}))

	// node-a holds both web pods, so the split it caused is visible there as
	// two classes where there was one. node-b is untouched by "tier".
	want = map[string][]kv.MapKey{
		"node-a": sorted(classKey(webFE, "app", "tier"), classKey(webBE, "app", "tier")),
		"node-b": sorted(classKey(db, "app", "tier"), classKey(unlabeled)),
	}
	if got := nodeClasses(pods); !mapsEqual(got, want) {
		t.Errorf("nodes after split = %v, want %v", got, want)
	}

	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app"}}))

	// Merging back must drop the classes that no longer exist rather than
	// leaving node-a holding a key no pod on it projects onto.
	want = map[string][]kv.MapKey{
		"node-a": {classKey(webFE, "app")},
		"node-b": sorted(classKey(db, "app"), classKey(unlabeled)),
	}
	if got := nodeClasses(pods); !mapsEqual(got, want) {
		t.Errorf("nodes after merge = %v, want %v", got, want)
	}
}

func TestUpdateRLabelsUnscheduledPod(t *testing.T) {
	pods := New()
	if err := pods.Init(testRLabels(map[string][]string{"ns1": {"app"}}), podSeq(
		testPod("ns1", "pending-1", "", webFE),
	)); err != nil {
		t.Fatalf("Init() = %v, want nil", err)
	}

	pods.UpdateRLabels(testRLabels(map[string][]string{"ns1": {"app", "tier"}}))

	// A pod the scheduler has not placed is in a class but on no node.
	if got, want := len(pods.nodes), 0; got != want {
		t.Errorf("len(nodes) = %v, want %v", got, want)
	}
	if got, want := len(pods.eqcToPod["ns1"]), 1; got != want {
		t.Errorf("len(eqc[ns1]) = %v, want %v", got, want)
	}
}

func TestTouchesKeys(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		keys   []string
		want   bool
	}{
		{"empty keys", webFE, nil, false},
		{"empty labels", nil, []string{"app"}, false},
		{"hit, labels smaller", webFE, []string{"app", "a", "b", "c"}, true},
		{"hit, keys smaller", webFE, []string{"tier"}, true},
		{"miss, labels smaller", webFE, []string{"a", "b", "c"}, false},
		{"miss, keys smaller", webFE, []string{"zone"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := touchesKeys(tc.labels, set.New(tc.keys...)); got != tc.want {
				t.Errorf("touchesKeys(%v, %v) = %v, want %v", tc.labels, tc.keys, got, tc.want)
			}
		})
	}
}

func sorted(keys ...kv.MapKey) []kv.MapKey {
	slices.Sort(keys)
	return keys
}

func mapsEqual[K comparable, V comparable](a, b map[K][]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || !slices.Equal(va, vb) {
			return false
		}
	}
	return true
}
