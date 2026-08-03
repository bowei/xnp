package store

import (
	"fmt"

	"github.com/bowei/xnp/internal/repr"
	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
)

// NetworkPolicies is the controller's view of the cluster's NetworkPolicies,
// held in the form policy resolution needs them: converted to repr, indexed by
// namespace, and summarized by the set of label keys they select on.
//
// NetworkPolicies is not safe for concurrent use.
type NetworkPolicies struct {
	// byNs indexes the policies by namespace and then by name. Resolution runs
	// per namespace, and a namespace's policies are the unit it works on, so
	// the namespace level is indexed rather than reached by filtering a flat
	// list.
	byNs map[string]map[string]*repr.NetworkPolicy

	// relevantKeys are all of the label keys that are references in
	// NetworkPolicies.
	relevantKeys set.Set[string]
}

func NewNetworkPolicies() *NetworkPolicies {
	return &NetworkPolicies{
		byNs:         map[string]map[string]*repr.NetworkPolicy{},
		relevantKeys: set.New[string](),
	}
}

// RelevantKeys is the set of label keys named by any selector of any stored
// policy. A key that no policy mentions cannot change whether a selector
// matches, so Pods and Namespaces only have to track the labels in this set.
//
// Pod and Namespace keys are not separated: a peer's namespaceSelector names
// Namespace labels and every other selector names Pod labels, but keeping one
// set costs only tracking a few keys that cannot matter for one of the two
// kinds, and never drops one that can.
//
// The returned set is the live one and must not be mutated.
func (nps *NetworkPolicies) RelevantKeys() set.Set[string] { return nps.relevantKeys }

// Init is run during the initial startup phase to populate the controller state
// in preparation for running the controller. It replaces whatever the store
// currently holds, so the caller hands over a synced NetworkPolicy cache and
// incremental updates take over from there.
//
// A policy that repr cannot represent (a named port, an unsupported selector
// operator, an unparseable CIDR) fails the whole Init rather than being
// skipped, since silently dropping a policy would render as more allowed
// traffic than the cluster asked for. The store is left untouched when that
// happens, so a failed Init cannot leave it holding half a cluster.
func (nps *NetworkPolicies) Init(
	policies func(yield func(*v1.NetworkPolicy) bool),
) error {
	byNs := map[string]map[string]*repr.NetworkPolicy{}
	relevantKeys := set.New[string]()

	var err error
	for np := range policies {
		var converted *repr.NetworkPolicy
		converted, err = repr.NewNetworkPolicy(np)
		if err != nil {
			// The iterator is stopped rather than returned from directly: a
			// range-over-func body cannot return for its caller, and running
			// it to completion after a failure would convert policies whose
			// results are already known to be discarded.
			break
		}

		inNs, ok := byNs[converted.Namespace]
		if !ok {
			inNs = map[string]*repr.NetworkPolicy{}
			byNs[converted.Namespace] = inNs
		}
		inNs[converted.Name] = converted

		addPolicyKeys(relevantKeys, np)
	}
	if err != nil {
		return fmt.Errorf("initializing NetworkPolicy store: %w", err)
	}

	nps.byNs = byNs
	nps.relevantKeys = relevantKeys
	return nil
}

// Get returns the named policy, and whether it is stored at all.
func (nps *NetworkPolicies) Get(ns, name string) (*repr.NetworkPolicy, bool) {
	np, ok := nps.byNs[ns][name]
	return np, ok
}

// Len is the number of policies stored.
func (nps *NetworkPolicies) Len() int {
	var n int
	for _, inNs := range nps.byNs {
		n += len(inNs)
	}
	return n
}

// Range returns an iterator over the policies in ns. The order is unspecified.
// The store must not be modified during iteration.
func (nps *NetworkPolicies) Range(ns string) func(yield func(*repr.NetworkPolicy) bool) {
	return func(yield func(*repr.NetworkPolicy) bool) {
		for _, np := range nps.byNs[ns] {
			if !yield(np) {
				return
			}
		}
	}
}

// All returns an iterator over every stored policy, in every namespace. The
// order is unspecified; a policy carries its own Namespace. The store must not
// be modified during iteration.
func (nps *NetworkPolicies) All() func(yield func(*repr.NetworkPolicy) bool) {
	return func(yield func(*repr.NetworkPolicy) bool) {
		for _, inNs := range nps.byNs {
			for _, np := range inNs {
				if !yield(np) {
					return
				}
			}
		}
	}
}

// addPolicyKeys adds every label key np selects on to keys.
//
// The keys are read off the API object rather than the compiled repr form: a
// Predicate is built for matching and drops terms that Build proves redundant,
// while a key stays relevant as long as some selector names it.
func addPolicyKeys(keys set.Set[string], np *v1.NetworkPolicy) {
	addSelectorKeys(keys, &np.Spec.PodSelector)
	for _, rule := range np.Spec.Ingress {
		addPeerKeys(keys, rule.From)
	}
	for _, rule := range np.Spec.Egress {
		addPeerKeys(keys, rule.To)
	}
}

func addPeerKeys(keys set.Set[string], peers []v1.NetworkPolicyPeer) {
	for _, peer := range peers {
		addSelectorKeys(keys, peer.PodSelector)
		addSelectorKeys(keys, peer.NamespaceSelector)
	}
}

// addSelectorKeys adds the keys sel names to keys. Every operator counts,
// including DoesNotExist and NotIn: an object is matched against its labels
// projected onto the relevant keys, so a key dropped from that projection
// would read as absent and satisfy an exclusion the real object fails.
func addSelectorKeys(keys set.Set[string], sel *metav1.LabelSelector) {
	if sel == nil {
		return
	}
	for key := range sel.MatchLabels {
		keys.Insert(key)
	}
	for _, expr := range sel.MatchExpressions {
		keys.Insert(expr.Key)
	}
}
