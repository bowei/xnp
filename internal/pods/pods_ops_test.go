package pods

import (
	"slices"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/repr/convert"
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

// classKey is the equivalence class labels projects onto under rlabels, in the
// form the eqc index is keyed by. The test builds its expectations the same way
// the store does rather than reaching for the entries' own EQC, so a wrong
// projection shows up as a missing class and not as agreement with itself.
func classKey(labels map[string]string, rlabels ...string) kv.MapKey {
	return convert.RelevantLabels(labels, set.New(rlabels...)).Comparable()
}

// classes is the eqc index of ns flattened to sorted member names per class.
func classes(t *testing.T, pods *Pods, ns string) map[kv.MapKey][]string {
	t.Helper()
	got := map[kv.MapKey][]string{}
	for key, members := range pods.eqc[ns] {
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
	return got
}

func nodeClasses(pods *Pods) map[string][]kv.MapKey {
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

func initPods(t *testing.T, rlabels ...string) *Pods {
	t.Helper()
	pods := NewPods()
	err := pods.Init(set.New(rlabels...), podSeq(
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
	pods := initPods(t, "app")

	// "app" alone cannot tell the two web pods apart.
	want := map[kv.MapKey][]string{
		classKey(webFE, "app"): {"web-1", "web-2"},
		classKey(db, "app"):    {"db-1"},
	}
	if got := classes(t, pods, "ns1"); !mapsEqual(got, want) {
		t.Errorf("classes(ns1) = %v, want %v", got, want)
	}

	pods.UpdateRLabels(set.New("app", "tier"))

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
	pods := initPods(t, "app", "tier")
	pods.UpdateRLabels(set.New("app"))

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

func TestUpdateRLabelsEmptyRelevantSet(t *testing.T) {
	pods := initPods(t, "app", "tier")
	pods.UpdateRLabels(set.New[string]())

	// With nothing relevant every pod in a namespace projects onto the empty
	// set, so a namespace is one class.
	want := map[kv.MapKey][]string{
		classKey(nil): {"db-1", "web-1", "web-2"},
	}
	if got := classes(t, pods, "ns1"); !mapsEqual(got, want) {
		t.Errorf("classes(ns1) = %v, want %v", got, want)
	}
	if got, want := len(pods.eqc["ns2"]), 1; got != want {
		t.Errorf("len(eqc[ns2]) = %v, want %v", got, want)
	}
}

func TestUpdateRLabelsNoChange(t *testing.T) {
	pods := initPods(t, "app")
	before := classes(t, pods, "ns1")

	// A key set that differs by an irrelevant key, then one that does not
	// differ at all: neither may move a pod.
	pods.UpdateRLabels(set.New("app", "nobody-carries-this"))
	if got := classes(t, pods, "ns1"); !mapsEqual(got, before) {
		t.Errorf("classes(ns1) after unused key = %v, want %v", got, before)
	}
	pods.UpdateRLabels(set.New("app", "nobody-carries-this"))
	if got := classes(t, pods, "ns1"); !mapsEqual(got, before) {
		t.Errorf("classes(ns1) after no-op = %v, want %v", got, before)
	}
}

func TestUpdateRLabelsRebuildsNodes(t *testing.T) {
	pods := initPods(t, "app")

	want := map[string][]kv.MapKey{
		"node-a": {classKey(webFE, "app")},
		"node-b": sorted(classKey(db, "app"), classKey(unlabeled, "app")),
	}
	if got := nodeClasses(pods); !mapsEqual(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}

	pods.UpdateRLabels(set.New("app", "tier"))

	// node-a holds both web pods, so the split it caused is visible there as
	// two classes where there was one. node-b is untouched by "tier".
	want = map[string][]kv.MapKey{
		"node-a": sorted(classKey(webFE, "app", "tier"), classKey(webBE, "app", "tier")),
		"node-b": sorted(classKey(db, "app", "tier"), classKey(unlabeled, "app", "tier")),
	}
	if got := nodeClasses(pods); !mapsEqual(got, want) {
		t.Errorf("nodes after split = %v, want %v", got, want)
	}

	pods.UpdateRLabels(set.New("app"))

	// Merging back must drop the classes that no longer exist rather than
	// leaving node-a holding a key no pod on it projects onto.
	want = map[string][]kv.MapKey{
		"node-a": {classKey(webFE, "app")},
		"node-b": sorted(classKey(db, "app"), classKey(unlabeled, "app")),
	}
	if got := nodeClasses(pods); !mapsEqual(got, want) {
		t.Errorf("nodes after merge = %v, want %v", got, want)
	}
}

func TestUpdateRLabelsUnscheduledPod(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		testPod("ns1", "pending-1", "", webFE),
	)); err != nil {
		t.Fatalf("Init() = %v, want nil", err)
	}

	pods.UpdateRLabels(set.New("app", "tier"))

	// A pod the scheduler has not placed is in a class but on no node.
	if got, want := len(pods.nodes), 0; got != want {
		t.Errorf("len(nodes) = %v, want %v", got, want)
	}
	if got, want := len(pods.eqc["ns1"]), 1; got != want {
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
