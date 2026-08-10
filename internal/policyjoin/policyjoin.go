// Package policyjoin resolves NetworkPolicies against pod equivalence classes:
// the EqcJoinPred step of doc/workflow.tex, sitting between the object stores
// and a renderer.
//
// The join is where the selectors stop being predicates and start being pairs.
// A policy names its subjects with a podSelector and its peers with more
// selectors; this evaluates all of them against the classes the pod store
// holds and emits one row per (subject class, peer class) pair a rule allows.
// Because the operands are classes and not pods, a rule between two 5,000-pod
// deployments is one row here rather than 25,000, and the fan-out back to pods
// is left to whatever renders per node.
package policyjoin

import (
	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/kvec/pkg/pred"
	"github.com/bowei/xnp/internal/npolicies"
	"github.com/bowei/xnp/internal/pods"
	networkingv1 "k8s.io/api/networking/v1"
)

// Direction is which way a rule's traffic runs relative to the pods its policy
// selects: those pods are the destination of an ingress rule and the source of
// an egress one.
type Direction uint8

const (
	DirectionIngress Direction = iota
	DirectionEgress
)

// entry is one row of the join: policy applies to one equivalence class in
// direction dir, and rule allows traffic between src and dest.
//
// One of src/dest is the subject -- the class the policy's own podSelector
// picked, dest for ingress and src for egress -- and is always non-nil, in the
// policy's own namespace. The other is the peer, in peerNs.
//
// The peer side is nil when the rule reaches something that is not a pod class:
// a rule with no peers at all, which allows every source, or one carrying an
// IPBlock. Both say "read rule", which carries the peers and the ports either
// way, so they are not distinguished here; len(rule.Peers) separates them.
//
// A nil rule is isolation with nothing to allow: the policy declares dir in its
// policyTypes but has no rules for it, so it renders as a default deny for the
// subject and nothing else. The row exists because without it such a policy --
// the plain default-deny, which is most of what a cluster runs -- would produce
// no entries at all and its subjects would read as unselected.
//
// peerNs is carried because a class is only identified within a namespace: two
// namespaces can hold pods projecting onto the very same labels and they are
// not the same peer. The subject's namespace is not carried, being policy's.
type entry struct {
	policy *npolicies.Entry
	rule   *npolicies.Rule
	dir    Direction
	src    *kv.KV
	dest   *kv.KV
	peerNs string
}

type Join struct {
	entries []entry
}

// Init is the startup path (doc/workflow.tex §Startup 4): it replaces whatever
// the Join holds with the full resolution of np against pods.
//
// nsLabels is the compiled label set of every namespace, keyed by name -- the
// same map npolicies.Init is handed, and built the same way, from the full
// label set rather than a projection. It is read but not retained. A peer that
// carries a namespaceSelector reaches into every namespace that selector
// matches, and neither store keeps the Namespace labels needed to say which
// those are; the caller holds the synced cache and passes it to both.
//
// The result is committed to the receiver only at the end, so a Join is either
// the old resolution or the new one and never half of each.
//
// Returns error to keep the startup paths one shape. Nothing here can fail:
// every selector was compiled, and rejected if it could not be, when its object
// was converted, and matching a compiled Predicate against a class is total.
func (j *Join) Init(
	np *npolicies.Store,
	pods *pods.Store,
	nsLabels map[string]*kv.KV,
) error {
	// The policies are collected before anything is matched rather than joined
	// as they are walked: All holds the policy store's read lock for the whole
	// walk, and reaching into the pod store from inside it would put an
	// ordering on two locks that nothing else in the controller establishes.
	var policies []*npolicies.Entry
	for e := range np.All {
		policies = append(policies, e)
	}

	jn := joiner{
		pods:     pods,
		nsLabels: nsLabels,
		classes:  map[string][]*kv.KV{},
	}
	for _, p := range policies {
		jn.policy(p)
	}

	j.entries = jn.entries

	return nil
}

// joiner is the state of one Init: the inputs being resolved against, the
// per-namespace class lists read out of the pod store along the way, and the
// rows produced so far.
type joiner struct {
	pods     *pods.Store
	nsLabels map[string]*kv.KV

	// classes caches the class list of each namespace already asked for. Every
	// policy in a namespace resolves its subjects against the same list, and a
	// namespaceSelector matching widely brings the whole cluster into range of
	// every rule that carries one, so without this the same list is re-read --
	// and the pod store's lock re-taken -- once per policy per rule.
	classes map[string][]*kv.KV

	entries []entry
}

// peerClass is one resolved peer: a class and the namespace that identifies it.
// The zero value is the peer side that is not a pod class at all.
type peerClass struct {
	ns    string
	class *kv.KV
}

// peerKey identifies a resolved peer for deduplication. Namespace qualified for
// the same reason entry.peerNs is: equal projections in two namespaces are two
// peers, not one.
type peerKey struct {
	ns    string
	class kv.MapKey
}

func (jn *joiner) policy(p *npolicies.Entry) {
	// A NetworkPolicy's podSelector never reaches outside the policy's own
	// namespace, so only that namespace's classes are candidates. Matching
	// against a projection rather than the pods' full labels is sound because
	// the policy registered its podSelector's keys against this namespace when
	// it was converted (npolicies.mergeKeys): no key this selector reads was
	// dropped from the classes it is being matched against.
	var subjects []*kv.KV
	for _, c := range jn.classesOf(p.Namespace) {
		if p.PodSelector.Match(c) {
			subjects = append(subjects, c)
		}
	}
	if len(subjects) == 0 {
		// The policy selects nothing that exists, so it neither allows nor
		// isolates anything and has no row to contribute. It stays in the
		// policy store regardless: a pod arriving into a class it matches is an
		// incremental update, not a reason to have carried an empty row here.
		return
	}

	// policyTypes is authoritative, not a summary of which rule lists are
	// populated: a direction absent from it neither isolates nor allows,
	// whatever the spec carries for it, so egress rules under a policy that
	// declares only Ingress are dead and are not walked. The apiserver defaults
	// the field from the rules present, so it is only empty on an object that
	// never went through one -- which then joins as a policy that does nothing,
	// the same reading the API gives it.
	for _, pt := range p.PolicyTypes {
		switch pt {
		case networkingv1.PolicyTypeIngress:
			jn.rules(p, subjects, DirectionIngress, p.Ingress)
		case networkingv1.PolicyTypeEgress:
			jn.rules(p, subjects, DirectionEgress, p.Egress)
		}
	}
}

func (jn *joiner) rules(
	p *npolicies.Entry,
	subjects []*kv.KV,
	dir Direction,
	rules []npolicies.Rule,
) {
	if len(rules) == 0 {
		for _, s := range subjects {
			jn.emit(p, nil, dir, s, peerClass{})
		}
		return
	}

	for i := range rules {
		// Resolved once per rule and reused across the subjects: which peers a
		// rule names does not depend on which class the policy selected, and a
		// rule reaching into every namespace costs a walk of the cluster to
		// answer.
		peers := jn.peerClasses(p.Namespace, &rules[i])
		for _, s := range subjects {
			for _, peer := range peers {
				jn.emit(p, &rules[i], dir, s, peer)
			}
		}
	}
}

func (jn *joiner) emit(
	p *npolicies.Entry,
	r *npolicies.Rule,
	dir Direction,
	subject *kv.KV,
	peer peerClass,
) {
	e := entry{policy: p, rule: r, dir: dir, peerNs: peer.ns}
	if dir == DirectionIngress {
		e.src, e.dest = peer.class, subject
	} else {
		e.src, e.dest = subject, peer.class
	}
	jn.entries = append(jn.entries, e)
}

// peerClasses resolves the peers of one rule to the classes they select.
//
// The result is deduplicated: two peers of a rule can select the same class --
// {app in (web, db)} alongside {app: web} -- and against a given subject they
// are the same row twice, which would render as a duplicate tuple.
func (jn *joiner) peerClasses(policyNs string, r *npolicies.Rule) []peerClass {
	// An empty peer list is not "selects no peers": it allows every source,
	// pod or otherwise, so there is no class that stands for it.
	if len(r.Peers) == 0 {
		return []peerClass{{}}
	}

	var out []peerClass

	// Only a rule naming more than one peer can reach a class twice: within a
	// single peer every namespace is visited once and a namespace's classes are
	// distinct already, so the pairs one peer produces cannot repeat. Most rules
	// carry one peer, and this is what keeps the map off their path.
	var seen map[peerKey]bool
	if len(r.Peers) > 1 {
		seen = map[peerKey]bool{}
	}

	var ipBlock bool

	for i := range r.Peers {
		p := &r.Peers[i]

		if p.IPBlock != nil {
			// An IPBlock peer selects addresses, not pods, and the API makes it
			// exclusive with the two selectors, so there is nothing else on
			// this peer to resolve. One row per rule is enough to send a
			// consumer to r.Peers, which holds every block the rule carries.
			ipBlock = true
			continue
		}

		if p.NamespaceSelector == nil {
			// An omitted namespaceSelector confines the peer to the policy's
			// own namespace. An empty one means the opposite -- every namespace
			// -- and falls through to the walk below, where a Predicate with no
			// terms matches all of them without a case of its own.
			jn.addClasses(&out, seen, policyNs, p.PodSelector)
			continue
		}
		for ns, labels := range jn.nsLabels {
			if p.NamespaceSelector.Match(labels) {
				jn.addClasses(&out, seen, ns, p.PodSelector)
			}
		}
	}

	if ipBlock {
		out = append(out, peerClass{})
	}
	return out
}

func (jn *joiner) addClasses(
	out *[]peerClass,
	seen map[peerKey]bool,
	ns string,
	sel *pred.Predicate,
) {
	for _, c := range jn.classesOf(ns) {
		// A nil podSelector is an omitted one, which selects every pod of the
		// namespaces the peer reaches rather than none of them.
		if sel != nil && !sel.Match(c) {
			continue
		}
		// The whole test is skipped rather than the lookup alone: a MapKey is
		// built by encoding the class, which is not something to pay for on the
		// path where nothing can be a duplicate.
		if seen != nil {
			key := peerKey{ns: ns, class: c.Comparable()}
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		*out = append(*out, peerClass{ns: ns, class: c})
	}
}

func (jn *joiner) classesOf(ns string) []*kv.KV {
	if cs, ok := jn.classes[ns]; ok {
		return cs
	}
	var cs []*kv.KV
	for c := range jn.pods.Classes(ns) {
		cs = append(cs, c)
	}
	// Cached even when empty, which is the answer a widely-matching
	// namespaceSelector runs into most: a namespace holding no pods would
	// otherwise miss the cache and pay a lock acquisition every time a rule
	// asks about it.
	jn.classes[ns] = cs
	return cs
}
