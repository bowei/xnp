package store

import (
	"fmt"

	"github.com/bowei/xnp/internal/eqc"
	"github.com/bowei/xnp/internal/repr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/set"
)

// Pods is the controller's view of the cluster's Pods, converted to repr,
// indexed by namespace, and grouped into equivalence classes by the labels
// policy actually selects on.
//
// Pods is not safe for concurrent use.
type Pods struct {
	// byNs indexes the pods by namespace and then by name, matching
	// NetworkPolicies: resolution runs per namespace, and a pod's identity in
	// the API is the (namespace, name) pair.
	byNs map[string]map[string]*repr.Pod

	// classes group the pods by their relevant labels. Whether a selector
	// matches a pod depends on nothing else, so it is decided once per class
	// rather than once per pod — the point of the whole arrangement, since the
	// pods in a namespace are overwhelmingly a few Deployments' worth of
	// identical label sets.
	classes eqc.Classes
}

func NewPods() *Pods {
	return &Pods{byNs: map[string]map[string]*repr.Pod{}}
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
	byNs := map[string]map[string]*repr.Pod{}
	var classes eqc.Classes

	var err error
	for pod := range podSeq {
		var converted repr.Pod
		converted, err = repr.NewPod(pod)
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
			inNs = map[string]*repr.Pod{}
			byNs[p.Namespace] = inNs
		}
		inNs[p.Name] = p

		p.SetClass(classes.Add(p.Namespace, p.Name, repr.RelevantLabels(p.Labels, relevantKeys)))
	}
	if err != nil {
		return fmt.Errorf("initializing Pod store: %w", err)
	}

	pods.byNs = byNs
	pods.classes = classes
	return nil
}

// Get returns the named pod, and whether it is stored at all.
func (pods *Pods) Get(ns, name string) (*repr.Pod, bool) {
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
func (pods *Pods) Range(ns string) func(yield func(*repr.Pod) bool) {
	return func(yield func(*repr.Pod) bool) {
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
func (pods *Pods) All() func(yield func(*repr.Pod) bool) {
	return func(yield func(*repr.Pod) bool) {
		for _, inNs := range pods.byNs {
			for _, p := range inNs {
				if !yield(p) {
					return
				}
			}
		}
	}
}
