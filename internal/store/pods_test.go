package store

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/bowei/xnp/internal/eqc"
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
