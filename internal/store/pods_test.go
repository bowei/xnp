package store

import (
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/bowei/xnp/internal/eqc"
	"github.com/bowei/xnp/internal/repr/podinternal"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
)

// podSeq is the iterator form Pods.Init takes, over a fixed list of pods.
func podSeq(pods ...*corev1.Pod) func(yield func(*corev1.Pod) bool) {
	return func(yield func(*corev1.Pod) bool) {
		for _, p := range pods {
			if !yield(p) {
				return
			}
		}
	}
}

// pod builds a Pod with the given labels and a single IP.
func pod(ns, name, ip string, labels map[string]string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
	}
	if ip != "" {
		p.Status.PodIP = ip
		p.Status.PodIPs = []corev1.PodIP{{IP: ip}}
	}
	return p
}

func TestNewPods(t *testing.T) {
	pods := NewPods()

	if got := pods.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if _, ok := pods.Get("ns1", "pod-1"); ok {
		t.Error("Get on an empty store = ok, want !ok")
	}
	// The classes are queried by resolution, which must not have to care
	// whether Init has run.
	for range pods.Classes().All() {
		t.Error("Classes on an empty store yielded a class")
	}
	for range pods.Range("ns1") {
		t.Error("Range on an empty store yielded a pod")
	}
	for range pods.All() {
		t.Error("All on an empty store yielded a pod")
	}
}

func TestPodsInit(t *testing.T) {
	pods := NewPods()
	err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
		pod("ns2", "web-1", "10.0.0.3", map[string]string{"app": "web"}),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if got, want := pods.Len(), 3; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}

	got, ok := pods.Get("ns1", "web-1")
	if !ok {
		t.Fatal("Get(ns1, web-1) = !ok, want ok")
	}
	if got.Namespace != "ns1" || got.Name != "web-1" {
		t.Errorf("Get(ns1, web-1) = %s/%s, want ns1/web-1", got.Namespace, got.Name)
	}
	if want := []netip.Addr{netip.MustParseAddr("10.0.0.1")}; !slices.Equal(got.IPs, want) {
		t.Errorf("Get(ns1, web-1).IPs = %v, want %v", got.IPs, want)
	}

	// Same name in two namespaces stays two pods, each indexed under the
	// namespace it came from.
	if got, ok := pods.Get("ns2", "web-1"); !ok {
		t.Error("Get(ns2, web-1) = !ok, want ok")
	} else if got.Namespace != "ns2" {
		t.Errorf("Get(ns2, web-1).Namespace = %q, want ns2", got.Namespace)
	}

	if _, ok := pods.Get("ns1", "nope"); ok {
		t.Error("Get(ns1, nope) = ok, want !ok")
	}
	if _, ok := pods.Get("nope", "web-1"); ok {
		t.Error("Get(nope, web-1) = ok, want !ok")
	}
}

func TestPodsInitNoIP(t *testing.T) {
	// A pod that has not been scheduled yet is stored rather than rejected: it
	// carries labels a selector can match, and its IP arrives by update.
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(pod("ns1", "pending", "", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	p, ok := pods.Get("ns1", "pending")
	if !ok {
		t.Fatal("Get(ns1, pending) = !ok, want ok")
	}
	if len(p.IPs) != 0 {
		t.Errorf("Get(ns1, pending).IPs = %v, want none", p.IPs)
	}
	if p.Class() == nil {
		t.Error("Get(ns1, pending).Class() = nil, want the pod linked to a class")
	}
}

func TestPodsInitLinksClasses(t *testing.T) {
	// The two web pods differ only in a label no policy names, so they project
	// onto the same relevant labels and have to land in one class — the whole
	// point of grouping by the projection rather than the full label set.
	pods := NewPods()
	err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "pod-template-hash": "abc"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web", "pod-template-hash": "def"}),
		pod("ns1", "db-1", "10.0.0.3", map[string]string{"app": "db"}),
		pod("ns2", "web-3", "10.0.0.4", map[string]string{"app": "web"}),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	class := func(ns, name string) *eqc.Class {
		p, ok := pods.Get(ns, name)
		if !ok {
			t.Fatalf("Get(%s, %s) = !ok, want ok", ns, name)
		}
		if p.Class() == nil {
			t.Fatalf("Get(%s, %s).Class() = nil, want a class", ns, name)
		}
		return p.Class()
	}

	if class("ns1", "web-1") != class("ns1", "web-2") {
		t.Error("pods differing only in an irrelevant label are in different classes, want one")
	}
	if class("ns1", "web-1") == class("ns1", "db-1") {
		t.Error("pods with different relevant labels share a class, want two")
	}
	// Classes are per namespace, so equal labels in two namespaces are still
	// two classes.
	if class("ns1", "web-1") == class("ns2", "web-3") {
		t.Error("pods in different namespaces share a class, want two")
	}

	var classes int
	for range pods.Classes().All() {
		classes++
	}
	if want := 3; classes != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", classes, want)
	}

	// The class a pod points at is the one the store's Classes hands back for
	// it, not a detached copy.
	cls, ok := pods.Classes().Get("ns1", "web-1")
	if !ok {
		t.Fatal("Classes().Get(ns1, web-1) = !ok, want ok")
	}
	if cls != class("ns1", "web-1") {
		t.Error("Classes().Get(ns1, web-1) is not the class the pod is linked to")
	}
	if got, want := cls.Len(), 2; got != want {
		t.Errorf("class Len() = %d, want %d", got, want)
	}
}

func TestPodsInitNoRelevantKeys(t *testing.T) {
	// With no policies in the cluster nothing is relevant, so every pod in a
	// namespace projects onto the empty label set and collapses into one class.
	pods := NewPods()
	err := pods.Init(set.New[string](), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var classes int
	for range pods.Classes().All() {
		classes++
	}
	if want := 1; classes != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", classes, want)
	}
}

func TestPodsInitReplacesPreviousContents(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(pod("ns1", "old", "10.0.0.1", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := pods.Init(set.New("app"), podSeq(pod("ns2", "new", "10.0.0.2", map[string]string{"app": "db"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, ok := pods.Get("ns1", "old"); ok {
		t.Error("Get(ns1, old) = ok after re-Init, want the previous contents dropped")
	}
	if _, ok := pods.Get("ns2", "new"); !ok {
		t.Error("Get(ns2, new) = !ok, want ok")
	}
	if got, want := pods.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	// The classes are rebuilt with the contents, not accumulated across Inits:
	// a stale class would hand resolution a pod that is no longer in the store.
	if _, ok := pods.Classes().Get("ns1", "old"); ok {
		t.Error("Classes().Get(ns1, old) = ok after re-Init, want the previous classes dropped")
	}
}

func TestPodsInitUnconvertiblePod(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(pod("ns1", "good", "10.0.0.1", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err := pods.Init(set.New("app"), podSeq(
		pod("ns2", "first", "10.0.0.2", map[string]string{"app": "web"}),
		pod("ns2", "bad", "not-an-ip", map[string]string{"app": "web"}),
		pod("ns2", "last", "10.0.0.3", map[string]string{"app": "web"}),
	))
	if err == nil {
		t.Fatal("Init with an unparseable pod IP = nil error, want an error")
	}

	// A failed Init leaves the store exactly as it was, rather than holding the
	// pods it managed to convert before hitting the bad one.
	if got, want := pods.Len(), 1; got != want {
		t.Errorf("Len() after a failed Init = %d, want %d", got, want)
	}
	if _, ok := pods.Get("ns1", "good"); !ok {
		t.Error("Get(ns1, good) = !ok after a failed Init, want the previous contents kept")
	}
	if _, ok := pods.Get("ns2", "first"); ok {
		t.Error("Get(ns2, first) = ok, want the partial results of a failed Init discarded")
	}
	if _, ok := pods.Classes().Get("ns2", "first"); ok {
		t.Error("Classes().Get(ns2, first) = ok, want the partial results of a failed Init discarded")
	}
}

func TestPodsInitStopsIteratingOnError(t *testing.T) {
	var yielded int
	counting := func(yield func(*corev1.Pod) bool) {
		for _, p := range []*corev1.Pod{
			pod("ns1", "first", "10.0.0.1", nil),
			pod("ns1", "bad", "not-an-ip", nil),
			pod("ns1", "last", "10.0.0.3", nil),
		} {
			yielded++
			if !yield(p) {
				return
			}
		}
	}

	pods := NewPods()
	if err := pods.Init(set.New("app"), counting); err == nil {
		t.Fatal("Init with an unparseable pod IP = nil error, want an error")
	}
	// The rest of the cache is not walked once the result is known to be
	// discarded.
	if want := 2; yielded != want {
		t.Errorf("pods yielded = %d, want %d", yielded, want)
	}
}

func TestPodsInitEmpty(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := pods.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	for range pods.Classes().All() {
		t.Error("Classes() after an empty Init yielded a class")
	}
}

// reprPod converts pod the way the watch path would before handing it to
// UpdatePod, failing the test on a pod the fixtures got wrong.
func reprPod(t *testing.T, p *corev1.Pod) *podinternal.Pod {
	t.Helper()
	converted, err := podinternal.NewPod(p)
	if err != nil {
		t.Fatalf("repr.NewPod(%s/%s): %v", p.Namespace, p.Name, err)
	}
	return &converted
}

func TestPodsUpdatePodAdds(t *testing.T) {
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// A pod the store has never seen joins a class it was not in before, so the
	// caller has to re-resolve.
	if !pods.UpdatePod(keys, reprPod(t, pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}))) {
		t.Error("UpdatePod adding a new pod = false, want true")
	}

	p, ok := pods.Get("ns1", "web-1")
	if !ok {
		t.Fatal("Get(ns1, web-1) = !ok after UpdatePod, want ok")
	}
	if p.Class() == nil {
		t.Fatal("Get(ns1, web-1).Class() = nil, want the pod linked to a class")
	}
	cls, ok := pods.Classes().Get("ns1", "web-1")
	if !ok {
		t.Fatal("Classes().Get(ns1, web-1) = !ok, want ok")
	}
	if cls != p.Class() {
		t.Error("Classes().Get(ns1, web-1) is not the class the pod is linked to")
	}
	if got, want := pods.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestPodsUpdatePodKeepsClass(t *testing.T) {
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq(
		pod("ns1", "web-1", "", map[string]string{"app": "web"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before, _ := pods.Get("ns1", "web-1")
	cls := before.Class()

	// The pod's IP arrives and an irrelevant label churns: neither touches the
	// projection, so the pod stays in its class and no policy needs re-resolving.
	updated := reprPod(t, pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "pod-template-hash": "abc"}))
	if pods.UpdatePod(keys, updated) {
		t.Error("UpdatePod that did not change the relevant labels = true, want false")
	}

	got, ok := pods.Get("ns1", "web-1")
	if !ok {
		t.Fatal("Get(ns1, web-1) = !ok, want ok")
	}
	if got != updated {
		t.Error("Get(ns1, web-1) is not the pod passed to UpdatePod, want the stored pod replaced")
	}
	if want := []netip.Addr{netip.MustParseAddr("10.0.0.1")}; !slices.Equal(got.IPs, want) {
		t.Errorf("Get(ns1, web-1).IPs = %v, want %v", got.IPs, want)
	}
	if got.Class() != cls {
		t.Error("Get(ns1, web-1).Class() changed, want the pod left in its class")
	}
	if got, want := cls.Len(), 2; got != want {
		t.Errorf("class Len() = %d, want %d", got, want)
	}
	if got, want := pods.Len(), 2; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestPodsUpdatePodMovesClass(t *testing.T) {
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web"}),
		pod("ns1", "db-1", "10.0.0.3", map[string]string{"app": "db"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	web, _ := pods.Get("ns1", "web-2")
	webClass := web.Class()
	db, _ := pods.Get("ns1", "db-1")
	dbClass := db.Class()

	if !pods.UpdatePod(keys, reprPod(t, pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "db"}))) {
		t.Error("UpdatePod that changed the relevant labels = false, want true")
	}

	moved, _ := pods.Get("ns1", "web-1")
	if moved.Class() != dbClass {
		t.Error("Get(ns1, web-1).Class() is not the class its new labels belong to")
	}
	if webClass.Has("web-1") {
		t.Error("the pod's old class still holds it, want it evicted")
	}
	if got, want := dbClass.Len(), 2; got != want {
		t.Errorf("new class Len() = %d, want %d", got, want)
	}
	if got, want := webClass.Len(), 1; got != want {
		t.Errorf("old class Len() = %d, want %d", got, want)
	}
}

func TestPodsUpdatePodDrainsEmptiedClass(t *testing.T) {
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq(pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// The only pod in its class leaves it: the class has to go with it, or
	// resolution walks a class with nothing behind it.
	if !pods.UpdatePod(keys, reprPod(t, pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "db"}))) {
		t.Error("UpdatePod that changed the relevant labels = false, want true")
	}

	var classes int
	for range pods.Classes().All() {
		classes++
	}
	if want := 1; classes != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", classes, want)
	}
}

func TestPodsUpdatePodIrrelevantLabelChange(t *testing.T) {
	// A label no policy names cannot move a pod between classes, whatever it
	// does — that is what makes the classes coarse enough to be worth having.
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq(pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "version": "1"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before, _ := pods.Get("ns1", "web-1")
	cls := before.Class()

	if pods.UpdatePod(keys, reprPod(t, pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "version": "2"}))) {
		t.Error("UpdatePod changing only an irrelevant label = true, want false")
	}
	after, _ := pods.Get("ns1", "web-1")
	if after.Class() != cls {
		t.Error("Get(ns1, web-1).Class() changed, want the pod left in its class")
	}
}

func TestPodsUpdatePodNewNamespace(t *testing.T) {
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq(pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if !pods.UpdatePod(keys, reprPod(t, pod("ns2", "web-1", "10.0.0.2", map[string]string{"app": "web"}))) {
		t.Error("UpdatePod adding a pod in a new namespace = false, want true")
	}

	// Same name, same labels, different namespace: two pods and two classes,
	// since resolution runs per namespace.
	ns1, ok := pods.Get("ns1", "web-1")
	if !ok {
		t.Fatal("Get(ns1, web-1) = !ok, want ok")
	}
	ns2, ok := pods.Get("ns2", "web-1")
	if !ok {
		t.Fatal("Get(ns2, web-1) = !ok, want ok")
	}
	if ns1.Class() == ns2.Class() {
		t.Error("pods in different namespaces share a class, want two")
	}
	if got, want := pods.Len(), 2; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestPodsDeletePod(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before, _ := pods.Get("ns1", "web-1")
	cls := before.Class()

	got, ok := pods.DeletePod("ns1", "web-1")
	if !ok {
		t.Fatal("DeletePod(ns1, web-1) = !ok, want ok")
	}
	if got != before {
		t.Error("DeletePod(ns1, web-1) returned a different pod than the store held")
	}
	// The returned pod still names the class it was resolved under, which is
	// what the caller invalidates.
	if got.Class() != cls {
		t.Error("DeletePod(ns1, web-1).Class() = nil or another class, want the class it was in")
	}

	if _, ok := pods.Get("ns1", "web-1"); ok {
		t.Error("Get(ns1, web-1) = ok after DeletePod, want !ok")
	}
	if _, ok := pods.Classes().Get("ns1", "web-1"); ok {
		t.Error("Classes().Get(ns1, web-1) = ok after DeletePod, want the pod dropped from its class")
	}
	if cls.Has("web-1") {
		t.Error("the class still holds the deleted pod, want it evicted")
	}
	if got, want := cls.Len(), 1; got != want {
		t.Errorf("class Len() = %d, want %d", got, want)
	}
	if got, want := pods.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	// The pod that shared the class is untouched by its neighbor leaving.
	if _, ok := pods.Get("ns1", "web-2"); !ok {
		t.Error("Get(ns1, web-2) = !ok, want the rest of the class kept")
	}
}

func TestPodsDeletePodDrainsClassAndNamespace(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns2", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, ok := pods.DeletePod("ns1", "web-1"); !ok {
		t.Fatal("DeletePod(ns1, web-1) = !ok, want ok")
	}

	// The last pod in a namespace takes the namespace's classes with it, or
	// resolution walks classes with nothing behind them.
	var classes int
	for range pods.Classes().All() {
		classes++
	}
	if want := 1; classes != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", classes, want)
	}
	for range pods.Classes().Range("ns1") {
		t.Error("Classes().Range(ns1) yielded a class after its only pod was deleted")
	}
	for range pods.Range("ns1") {
		t.Error("Range(ns1) yielded a pod after its only pod was deleted")
	}

	var all []string
	for p := range pods.All() {
		all = append(all, p.Namespace+"/"+p.Name)
	}
	if want := []string{"ns2/db-1"}; !slices.Equal(all, want) {
		t.Errorf("All() = %v, want %v", all, want)
	}
}

func TestPodsDeletePodUnknown(t *testing.T) {
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// A delete for a pod the store never held — a duplicate event, a pod whose
	// add was never seen — is a no-op rather than a disturbance.
	if p, ok := pods.DeletePod("ns1", "nope"); ok || p != nil {
		t.Errorf("DeletePod(ns1, nope) = %v, %v, want nil, false", p, ok)
	}
	if p, ok := pods.DeletePod("nope", "web-1"); ok || p != nil {
		t.Errorf("DeletePod(nope, web-1) = %v, %v, want nil, false", p, ok)
	}
	if _, ok := pods.DeletePod("ns1", "web-1"); !ok {
		t.Fatal("DeletePod(ns1, web-1) = !ok, want ok")
	}
	if p, ok := pods.DeletePod("ns1", "web-1"); ok || p != nil {
		t.Errorf("DeletePod(ns1, web-1) repeated = %v, %v, want nil, false", p, ok)
	}
	if got := pods.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
}

func TestPodsDeletePodThenUpdate(t *testing.T) {
	// A pod recreated after a delete is an add again: it has no previous class,
	// so the update reports a change and the store holds only the new pod.
	keys := set.New("app")
	pods := NewPods()
	if err := pods.Init(keys, podSeq(pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, ok := pods.DeletePod("ns1", "web-1"); !ok {
		t.Fatal("DeletePod(ns1, web-1) = !ok, want ok")
	}

	recreated := reprPod(t, pod("ns1", "web-1", "10.0.0.9", map[string]string{"app": "web"}))
	if !pods.UpdatePod(keys, recreated) {
		t.Error("UpdatePod re-adding a deleted pod = false, want true")
	}
	got, ok := pods.Get("ns1", "web-1")
	if !ok {
		t.Fatal("Get(ns1, web-1) = !ok after re-adding, want ok")
	}
	if got != recreated {
		t.Error("Get(ns1, web-1) is not the re-added pod")
	}
	if got.Class() == nil {
		t.Error("Get(ns1, web-1).Class() = nil, want the pod linked to a class")
	}
	if got, want := pods.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

// podClass is the class the named pod is linked to, failing the test if the pod
// is missing or unlinked.
func podClass(t *testing.T, pods *Pods, ns, name string) *eqc.Class {
	t.Helper()
	p, ok := pods.Get(ns, name)
	if !ok {
		t.Fatalf("Get(%s, %s) = !ok, want ok", ns, name)
	}
	if p.Class() == nil {
		t.Fatalf("Get(%s, %s).Class() = nil, want a class", ns, name)
	}
	return p.Class()
}

// countClasses is the number of classes the store currently groups its pods
// into, across every namespace.
func countClasses(pods *Pods) int {
	var n int
	for range pods.Classes().All() {
		n++
	}
	return n
}

func TestPodsUpdateRelevantPodsSplits(t *testing.T) {
	// The two web pods are one class while only "app" is selected on. A policy
	// arrives that selects on "role", and they have to come apart: the labels
	// that separate them are suddenly ones a selector can see.
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "role": "fe"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web", "role": "be"}),
		pod("ns1", "db-1", "10.0.0.3", map[string]string{"app": "db"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got, want := countClasses(pods), 2; got != want {
		t.Fatalf("Classes().All() yielded %d classes before the key set changed, want %d", got, want)
	}
	dbClass := podClass(t, pods, "ns1", "db-1")

	pods.UpdateRelevantPods(set.New("app", "role"))

	if podClass(t, pods, "ns1", "web-1") == podClass(t, pods, "ns1", "web-2") {
		t.Error("pods differing in a now-relevant label share a class, want two")
	}
	if got, want := countClasses(pods), 3; got != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", got, want)
	}
	// The db pod carries no "role" at all, so its projection is what it was and
	// it keeps the class it was resolved under.
	if podClass(t, pods, "ns1", "db-1") != dbClass {
		t.Error("a pod whose projection did not change was moved to a new class, want it left alone")
	}
	// The classes the store hands out and the links the pods hold stay one view.
	for _, name := range []string{"web-1", "web-2", "db-1"} {
		cls, ok := pods.Classes().Get("ns1", name)
		if !ok {
			t.Fatalf("Classes().Get(ns1, %s) = !ok, want ok", name)
		}
		if cls != podClass(t, pods, "ns1", name) {
			t.Errorf("Classes().Get(ns1, %s) is not the class the pod is linked to", name)
		}
	}
}

func TestPodsUpdateRelevantPodsMerges(t *testing.T) {
	// The mirror image: the policy that selected on "role" is deleted, the key
	// stops being relevant, and the classes it separated collapse back together.
	pods := NewPods()
	if err := pods.Init(set.New("app", "role"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "role": "fe"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web", "role": "be"}),
		pod("ns1", "db-1", "10.0.0.3", map[string]string{"app": "db"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got, want := countClasses(pods), 3; got != want {
		t.Fatalf("Classes().All() yielded %d classes before the key set changed, want %d", got, want)
	}

	pods.UpdateRelevantPods(set.New("app"))

	if podClass(t, pods, "ns1", "web-1") != podClass(t, pods, "ns1", "web-2") {
		t.Error("pods differing only in a no-longer-relevant label are in different classes, want one")
	}
	if podClass(t, pods, "ns1", "web-1") == podClass(t, pods, "ns1", "db-1") {
		t.Error("pods with different relevant labels share a class, want two")
	}
	// The classes that drained as their pods merged are gone, not left empty.
	if got, want := countClasses(pods), 2; got != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", got, want)
	}
	if got, want := podClass(t, pods, "ns1", "web-1").Len(), 2; got != want {
		t.Errorf("merged class Len() = %d, want %d", got, want)
	}
}

func TestPodsUpdateRelevantPodsNoEffect(t *testing.T) {
	// A key set that grew by a key no pod carries changes no projection, so
	// every class survives and nothing the caller has resolved per class is
	// invalidated by the re-grouping itself.
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web"}),
		pod("ns2", "db-1", "10.0.0.3", map[string]string{"app": "db"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before := map[string]*eqc.Class{
		"ns1/web-1": podClass(t, pods, "ns1", "web-1"),
		"ns1/web-2": podClass(t, pods, "ns1", "web-2"),
		"ns2/db-1":  podClass(t, pods, "ns2", "db-1"),
	}

	pods.UpdateRelevantPods(set.New("app", "tier"))

	for _, key := range []string{"ns1/web-1", "ns1/web-2", "ns2/db-1"} {
		ns, name, _ := strings.Cut(key, "/")
		if podClass(t, pods, ns, name) != before[key] {
			t.Errorf("%s was moved to a new class, want it left in place", key)
		}
	}
	if got, want := countClasses(pods), 2; got != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", got, want)
	}
}

func TestPodsUpdateRelevantPodsToEmpty(t *testing.T) {
	// With the last policy gone nothing is relevant: every pod in a namespace
	// projects onto the empty label set, and a namespace is one class.
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
		pod("ns2", "web-2", "10.0.0.3", map[string]string{"app": "web"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pods.UpdateRelevantPods(set.New[string]())

	if podClass(t, pods, "ns1", "web-1") != podClass(t, pods, "ns1", "db-1") {
		t.Error("pods in one namespace are in different classes with no relevant keys, want one")
	}
	// Classes are per namespace, so an empty projection does not collapse the
	// cluster into a single class.
	if podClass(t, pods, "ns1", "web-1") == podClass(t, pods, "ns2", "web-2") {
		t.Error("pods in different namespaces share a class, want two")
	}
	if got, want := countClasses(pods), 2; got != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", got, want)
	}
}

func TestPodsUpdateRelevantPodsLeavesPods(t *testing.T) {
	// Re-grouping touches the grouping and nothing else: the pods are the same
	// objects, with the same raw labels the next projection will be built from.
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "role": "fe"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before, _ := pods.Get("ns1", "web-1")

	pods.UpdateRelevantPods(set.New("role"))

	got, ok := pods.Get("ns1", "web-1")
	if !ok {
		t.Fatal("Get(ns1, web-1) = !ok, want ok")
	}
	if got != before {
		t.Error("Get(ns1, web-1) is a different pod after re-grouping, want the stored pod kept")
	}
	if want := []netip.Addr{netip.MustParseAddr("10.0.0.1")}; !slices.Equal(got.IPs, want) {
		t.Errorf("Get(ns1, web-1).IPs = %v, want %v", got.IPs, want)
	}
	if want := map[string]string{"app": "web", "role": "fe"}; !maps.Equal(got.Labels, want) {
		t.Errorf("Get(ns1, web-1).Labels = %v, want %v", got.Labels, want)
	}
	if got, want := pods.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestPodsUpdateRelevantPodsEmptyStore(t *testing.T) {
	pods := NewPods()
	pods.UpdateRelevantPods(set.New("app"))
	if got := pods.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if got := countClasses(pods); got != 0 {
		t.Errorf("Classes().All() yielded %d classes, want 0", got)
	}
}

func TestPodsUpdateRelevantPodsThenUpdatePod(t *testing.T) {
	// After a re-grouping the per-pod path has to agree with it: a pod updated
	// under the new key set lands in the class the walk put its peers in, or the
	// two paths have split the store into two disagreeing views.
	pods := NewPods()
	if err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web", "role": "fe"}),
		pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web", "role": "be"}),
	)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	keys := set.New("app", "role")
	pods.UpdateRelevantPods(keys)
	feClass := podClass(t, pods, "ns1", "web-1")

	// web-2 is re-labelled into the front end and has to join web-1's class.
	if !pods.UpdatePod(keys, reprPod(t, pod("ns1", "web-2", "10.0.0.2", map[string]string{"app": "web", "role": "fe"}))) {
		t.Error("UpdatePod that changed the relevant labels = false, want true")
	}
	if podClass(t, pods, "ns1", "web-2") != feClass {
		t.Error("Get(ns1, web-2).Class() is not the class its new labels belong to")
	}
	if got, want := countClasses(pods), 1; got != want {
		t.Errorf("Classes().All() yielded %d classes, want %d", got, want)
	}
	if got, want := feClass.Len(), 2; got != want {
		t.Errorf("class Len() = %d, want %d", got, want)
	}
}

func TestPodsRangeAndAll(t *testing.T) {
	pods := NewPods()
	err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", map[string]string{"app": "web"}),
		pod("ns1", "db-1", "10.0.0.2", map[string]string{"app": "db"}),
		pod("ns2", "web-2", "10.0.0.3", map[string]string{"app": "web"}),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var inNs1 []string
	for p := range pods.Range("ns1") {
		if p.Namespace != "ns1" {
			t.Errorf("Range(ns1) yielded a pod in %q", p.Namespace)
		}
		inNs1 = append(inNs1, p.Name)
	}
	slices.Sort(inNs1)
	if want := []string{"db-1", "web-1"}; !slices.Equal(inNs1, want) {
		t.Errorf("Range(ns1) = %v, want %v", inNs1, want)
	}

	for range pods.Range("nope") {
		t.Error("Range on an unknown namespace yielded a pod")
	}

	var all []string
	for p := range pods.All() {
		all = append(all, p.Namespace+"/"+p.Name)
	}
	slices.Sort(all)
	if want := []string{"ns1/db-1", "ns1/web-1", "ns2/web-2"}; !slices.Equal(all, want) {
		t.Errorf("All() = %v, want %v", all, want)
	}
}

func TestPodsRangeAndAllStopEarly(t *testing.T) {
	pods := NewPods()
	err := pods.Init(set.New("app"), podSeq(
		pod("ns1", "web-1", "10.0.0.1", nil),
		pod("ns1", "db-1", "10.0.0.2", nil),
		pod("ns2", "web-2", "10.0.0.3", nil),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	var ranged int
	for range pods.Range("ns1") {
		ranged++
		break
	}
	if want := 1; ranged != want {
		t.Errorf("Range(ns1) with a break yielded %d, want %d", ranged, want)
	}

	var seen int
	for range pods.All() {
		seen++
		break
	}
	if want := 1; seen != want {
		t.Errorf("All() with a break yielded %d, want %d", seen, want)
	}
}
