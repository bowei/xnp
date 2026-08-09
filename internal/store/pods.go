package store

import (
	"fmt"
	"maps"
	"slices"

	"github.com/bowei/xnp/internal/eqc"
	"github.com/bowei/xnp/internal/repr/convert"
	"github.com/bowei/xnp/internal/repr/podinternal"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/set"
)

type podMap map[string]map[string]*podinternal.Pod

// Pods is the controller's view of the cluster's Pods, converted to repr,
// indexed by namespace, and grouped into equivalence classes by the labels
// policy actually selects on.
//
// Pods is not safe for concurrent use.
type Pods struct {
	// byNs indexes the pods by namespace and then by name, matching
	// NetworkPolicies: resolution runs per namespace, and a pod's identity in
	// the API is the (namespace, name) pair.
	byNs podMap

	// classes group the pods by their relevant labels. Whether a selector
	// matches a pod depends on nothing else, so it is decided once per class
	// rather than once per pod — the point of the whole arrangement, since the
	// pods in a namespace are overwhelmingly a few Deployments' worth of
	// identical label sets.
	classes eqc.Classes
}

func NewPods() *Pods {
	return &Pods{
		byNs: podMap{},
	}
}

// labelKeys is the pod's label keys, handed to eqc alongside the projection it
// is grouped by. The class counts them so that eqc.Class.HasKey can answer for
// a key outside the projection — the question a change to the relevant keys
// asks, since a class can only split over a key its objects actually carry.
//
// A fresh slice rather than a view of the pod's labels: eqc holds onto it, and
// the label map is shared with the API object and may be replaced under it.
func labelKeys(labels map[string]string) []string {
	return slices.Collect(maps.Keys(labels))
}

// Init is the startup path (doc/workflow.tex §Startup). It replaces whatever
// the store currently holds, so the caller hands over a synced Pod cache and
// incremental updates take over from there.
//
// relevantKeys is NetworkPolicies.RelevantKeys, which is why the NetworkPolicy
// store has to be initialized first: a pod is grouped by its labels projected
// onto that set, and the projection cannot be built before the set is known.
//
// A pod that repr cannot represent (an unparseable IP) fails the whole Init
// rather than being skipped, for the reason NetworkPolicies.Init does the same:
// a pod missing from the store is a pod no policy selects, which renders as
// more allowed traffic than the cluster asked for. The store is left untouched
// when that happens.
func (pods *Pods) Init(
	relevantKeys set.Set[string],
	podSeq func(yield func(*corev1.Pod) bool),
) error {
	byNs := podMap{}
	var classes eqc.Classes

	var err error
	for pod := range podSeq {
		var converted podinternal.Pod
		converted, err = podinternal.NewPod(pod)
		if err != nil {
			// The iterator is stopped rather than returned from directly: a
			// range-over-func body cannot return for its caller, and running it
			// to completion after a failure would convert pods whose results
			// are already known to be discarded.
			break
		}

		p := &converted
		inNs, ok := byNs[p.Namespace]
		if !ok {
			inNs = map[string]*podinternal.Pod{}
			byNs[p.Namespace] = inNs
		}
		inNs[p.Name] = p

		p.SetClass(classes.Add(eqc.Entry{
			Namespace: p.Namespace,
			Name:      p.Name,
			KV:        convert.RelevantLabels(p.Labels, relevantKeys),
			AllKeys:   labelKeys(p.Labels),
		}))
	}
	if err != nil {
		return fmt.Errorf("initializing Pod store: %w", err)
	}

	pods.byNs = byNs
	pods.classes = classes
	return nil
}

// Get returns the named pod, and whether it is stored at all.
func (pods *Pods) Get(ns, name string) (*podinternal.Pod, bool) {
	p, ok := pods.byNs[ns][name]
	return p, ok
}

// Len is the number of pods stored.
func (pods *Pods) Len() int {
	var n int
	for _, inNs := range pods.byNs {
		n += len(inNs)
	}
	return n
}

// Classes are the equivalence classes the stored pods are grouped into.
// Resolution walks these rather than the pods themselves; a class names its
// members, so the pods behind a match are reachable through Get.
//
// The returned Classes are the live ones and must not be mutated by the caller:
// the pods' links into them are only correct while the store owns the updates.
func (pods *Pods) Classes() *eqc.Classes { return &pods.classes }

// Range returns an iterator over the pods in ns. The order is unspecified. The
// store must not be modified during iteration.
func (pods *Pods) Range(ns string) func(yield func(*podinternal.Pod) bool) {
	return func(yield func(*podinternal.Pod) bool) {
		for _, p := range pods.byNs[ns] {
			if !yield(p) {
				return
			}
		}
	}
}

// All returns an iterator over every stored pod, in every namespace. The order
// is unspecified; a pod carries its own Namespace. The store must not be
// modified during iteration.
func (pods *Pods) All() func(yield func(*podinternal.Pod) bool) {
	return func(yield func(*podinternal.Pod) bool) {
		for _, inNs := range pods.byNs {
			for _, p := range inNs {
				if !yield(p) {
					return
				}
			}
		}
	}
}

// UpdatePod is the incremental upsert path (doc/workflow.tex §Update/Pod). It
// stores pod under its (namespace, name), replacing whatever was held there,
// and re-groups it into the class for its relevant labels. A pod the store has
// not seen is added, so an upsert that arrives before its add — a resync, a
// watch that started mid-stream — does not lose the pod.
//
// The pod is taken already converted, unlike Init: an unconvertible pod is a
// failure the caller has to decide about (the pod must not silently vanish from
// the store, since a pod no policy selects renders as more allowed traffic than
// the cluster asked for), and there is nothing this store can do about it.
//
// relevantKeys is NetworkPolicies.RelevantKeys, for the same reason Init takes
// it: the pod is grouped by its labels projected onto that set. Passing a key
// set that has changed since the last call does not re-group the rest of the
// store — a changed key set invalidates every pod's projection, so it is a
// whole-store operation rather than something one pod's update can carry.
//
// Reports whether the pod's equivalence class changed, which is the signal a
// caller marking resolved policies for update needs: policy is resolved once
// per class, so a pod landing back in the class it was already in leaves every
// selector's answer intact, while an added pod, a new class, or a move between
// classes does not. A change confined to the pod itself — most often its IPs
// arriving — reports false: that re-renders the pod, it does not re-resolve
// policy.
func (pods *Pods) UpdatePod(
	relevantKeys set.Set[string],
	pod *podinternal.Pod,
) bool {
	inNs, ok := pods.byNs[pod.Namespace]
	if !ok {
		inNs = map[string]*podinternal.Pod{}
		pods.byNs[pod.Namespace] = inNs
	}

	// The class is read off the pod being replaced rather than from classes:
	// the two agree, and this is the comparison the result is about. A pod the
	// store has not seen has no previous class, so any class it lands in is a
	// change.
	var prev *eqc.Class
	if old, ok := inNs[pod.Name]; ok {
		prev = old.Class()
	}
	inNs[pod.Name] = pod

	cls := pods.classes.Update(eqc.Entry{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		KV:        convert.RelevantLabels(pod.Labels, relevantKeys),
		AllKeys:   labelKeys(pod.Labels),
	})
	pod.SetClass(cls)
	return cls != prev
}

// DeletePod drops the named pod from the store and from its equivalence class
// (doc/workflow.tex §Update/Pod/Delete), emptying and removing the class if it
// was the last pod in it. Deleting a pod the store does not hold is a no-op.
//
// No relevant keys are needed here, unlike UpdatePod: the pod is found by name
// and the class it sits in is already known, so nothing has to be projected.
//
// The removed pod is returned along with whether it was stored at all. A caller
// marking resolved policies for update needs the class the pod was in, and
// after the delete there is no other way to reach it — so the returned pod
// keeps its Class() link rather than having it cleared, even though that class
// may no longer be in Classes(). It is the identity of the resolution to
// invalidate, not a live handle: nothing may be read from it but its identity,
// since a drained class has already been dropped.
func (pods *Pods) DeletePod(ns, name string) (*podinternal.Pod, bool) {
	inNs, ok := pods.byNs[ns]
	if !ok {
		return nil, false
	}
	p, ok := inNs[name]
	if !ok {
		return nil, false
	}

	delete(inNs, name)
	if len(inNs) == 0 {
		// Namespaces are pruned as they empty, as eqc prunes its own, so that
		// the cost of walking the store tracks the live pods rather than every
		// namespace that ever held one.
		delete(pods.byNs, ns)
	}
	pods.classes.Delete(ns, name)
	return p, true
}

// UpdateRelevantPods re-groups every stored pod onto a new set of relevant
// keys. This is the whole-store operation neither UpdatePod nor DeletePod can
// carry: the keys are a property of the NetworkPolicy store, so a policy
// arriving or leaving can change them, and a changed key set invalidates every
// pod's projection at once rather than any one pod's.
//
// The pods themselves are untouched. They keep their labels raw precisely for
// this — the projection is the only thing that goes stale — so re-grouping
// reads what is already stored and needs nothing from the API.
//
// The classes are updated in place rather than rebuilt from scratch. Both cost
// one walk of the store, but updating keeps the identity of the classes that
// survive it: a key set almost always changes by a key or two, most pods
// project onto exactly what they projected onto before, and eqc leaves those
// where they are. A rebuild would hand every one of them a brand new Class and
// so invalidate resolutions that are still perfectly good.
//
// Pods are re-grouped one at a time, so partway through the walk a class can
// hold pods projected onto the old key set beside pods projected onto the new
// one. That is transient and self-correcting: a class is identified by its
// key/value set, not by when its members arrived, and every pod that does not
// belong under the new set is moved off before the walk ends.
func (pods *Pods) UpdateRelevantPods(relevantKeys set.Set[string]) {
	for p := range pods.All() {
		p.SetClass(pods.classes.Update(eqc.Entry{
			Namespace: p.Namespace,
			Name:      p.Name,
			KV:        convert.RelevantLabels(p.Labels, relevantKeys),
			AllKeys:   labelKeys(p.Labels),
		}))
	}
}
