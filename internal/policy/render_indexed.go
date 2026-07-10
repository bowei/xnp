package policy

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/repr"
)

// RenderIndexed is equivalent to RenderBasic but avoids brute-force
// selector×pod evaluation, per the "Label optimizations" in
// doc/optimizations.md, using repr's matching-layer types (repr.LabelSet,
// repr.CompiledSelector, repr.InvertedIndex, repr.Bitset, ...):
//
//   - Pods are canonicalized into small-integer label-set identities (#1);
//     selector evaluation runs once per (namespace, identity) group and is
//     fanned out to every pod sharing it, instead of once per pod.
//   - Selectors are compiled once into exact/residual parts (#2) and matched
//     via an inverted index (#3) instead of re-running
//     labels.Selector.Matches per candidate.
//   - Namespace pod buckets plus a namespace-selector cache (#5) avoid
//     rescanning all pods/namespaces per rule.
//   - Match results are stored as bitmaps (#8) for O(1) membership tests.
//
// Output tuple set is identical to RenderBasic's (see the differential
// tests in render_indexed_test.go and the cross-checks in render_test.go),
// though not necessarily in the same order.
//
// Tuple count per pod is knowable from its group template alone (ingress +
// egress entry count × own-IP count), so this sizes the result slice exactly
// once up front and writes into it directly, the same way
// materializeTuplesParallel does for RenderIndexedParallel — as opposed to
// growing tuples via append, which at this output size (tens of millions of
// Tuples for large clusters) burns several times the final live size in
// doubling-and-copy churn along the way.
func RenderIndexed(in *Input) ([]Tuple, error) {
	st, err := prepareIndexedRender(in)
	if err != nil {
		return nil, err
	}

	groupCache := map[groupKey]*groupTemplates{}
	templates := make([]*groupTemplates, len(st.sortedPods))
	ownIPs := make([][]string, len(st.sortedPods))
	offsets := make([]int, len(st.sortedPods)+1)

	for i, pod := range st.sortedPods {
		key := groupKey{pod.Namespace, st.podIdentity[i]}
		tmpl, ok := groupCache[key]
		if !ok {
			tmpl, err = st.groupTemplatesFor(key)
			if err != nil {
				return nil, fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
			}
			groupCache[key] = tmpl
		}
		templates[i] = tmpl

		ips := podIPStrings(pod)
		ownIPs[i] = ips
		n := len(ips)
		if n == 0 {
			n = 1
		}
		offsets[i+1] = offsets[i] + (len(tmpl.ingress)+len(tmpl.egress))*n
	}

	tuples := make([]Tuple, offsets[len(st.sortedPods)])
	for i, pod := range st.sortedPods {
		tmpl := templates[i]
		podName := types.NamespacedName{Namespace: in.Table.String(pod.Namespace), Name: pod.Name}
		dst := tuples[offsets[i]:offsets[i+1]]
		n := 0
		for _, tt := range tmpl.ingress {
			n += writeTuples(dst[n:], podName, ownIPs[i], DirectionIngress, tt.verdict, tt.peer, tt.port)
		}
		for _, tt := range tmpl.egress {
			n += writeTuples(dst[n:], podName, ownIPs[i], DirectionEgress, tt.verdict, tt.peer, tt.port)
		}
	}

	return tuples, nil
}

// groupKey identifies a (namespace, identity) group: every pod sharing a key
// resolves to the same ingress/egress templates.
type groupKey struct {
	ns repr.StringID
	id repr.LabelSetID
}

// indexedState holds every structure RenderIndexed builds once up front —
// the compiled selector registry, the inverted index's per-identity match
// bitmaps, and the namespace/identity pod buckets — so it can be shared
// between RenderIndexed and RenderIndexedParallel, which differ only in how
// they walk groups and pods once this state exists.
type indexedState struct {
	in                  *Input
	sortedPods          []repr.Pod
	podIdentity         []repr.LabelSetID
	policiesByNamespace map[repr.StringID][]int
	policySelector      []repr.SelectorID // indexed by position in in.Policies
	registry            *repr.SelectorRegistry
	matchedByIdentity   []repr.Bitset
	identitiesByNS      map[repr.StringID][]repr.LabelSetID
	podsByNS            map[repr.StringID]map[repr.LabelSetID][]repr.Pod
	nsIndex             *repr.NamespaceSelectorCache
}

func prepareIndexedRender(in *Input) (*indexedState, error) {
	sortedPods, _, policiesByNamespace := prepareRender(in)

	registry := repr.NewSelectorRegistry(in.Table)

	// Register every PodSelector (policy-level and peer-level) up front, so
	// the inverted index and the per-identity match bitmaps below are
	// computed exactly once, covering every selector that will ever be
	// probed during rendering.
	policySelector, err := registerAllSelectors(in.Policies, registry)
	if err != nil {
		return nil, err
	}
	index := repr.BuildInvertedIndex(registry.All())

	identities := repr.NewIdentityAllocator()
	podIdentity := make([]repr.LabelSetID, len(sortedPods))
	podsByNS := map[repr.StringID]map[repr.LabelSetID][]repr.Pod{}
	for i, pod := range sortedPods {
		id := identities.Intern(pod.Labels)
		podIdentity[i] = id
		byID := podsByNS[pod.Namespace]
		if byID == nil {
			byID = map[repr.LabelSetID][]repr.Pod{}
			podsByNS[pod.Namespace] = byID
		}
		byID[id] = append(byID[id], pod)
	}
	identitiesByNS := map[repr.StringID][]repr.LabelSetID{}
	for ns, byID := range podsByNS {
		ids := make([]repr.LabelSetID, 0, len(byID))
		for id := range byID {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		identitiesByNS[ns] = ids
	}

	// The core payoff of identity classes: this loop runs once per distinct
	// label set, not once per pod. For a cluster where replicas dominate
	// (e.g. 5,000 pods / 200 distinct label sets), that's a ~25x cut in
	// selector evaluations before a single Tuple is produced.
	matchedByIdentity := make([]repr.Bitset, identities.Len())
	for id := 0; id < identities.Len(); id++ {
		bs := repr.NewBitset(len(registry.All()))
		for _, sid := range index.Match(identities.Get(repr.LabelSetID(id))) {
			bs.Set(uint32(sid))
		}
		matchedByIdentity[id] = bs
	}

	nsIndex := repr.NewNamespaceSelectorCache(in.Namespaces, in.Table)

	return &indexedState{
		in:                  in,
		sortedPods:          sortedPods,
		podIdentity:         podIdentity,
		policiesByNamespace: policiesByNamespace,
		policySelector:      policySelector,
		registry:            registry,
		matchedByIdentity:   matchedByIdentity,
		identitiesByNS:      identitiesByNS,
		podsByNS:            podsByNS,
		nsIndex:             nsIndex,
	}, nil
}

func (st *indexedState) groupTemplatesFor(key groupKey) (*groupTemplates, error) {
	return computeGroupTemplates(st.in, st.matchedByIdentity[key.id], st.policiesByNamespace[key.ns], st.policySelector, st.registry, st.matchedByIdentity, st.identitiesByNS, st.podsByNS, st.nsIndex)
}

// registerAllSelectors pre-compiles every PodSelector referenced by
// policies (both the policy-level selector and every peer's selector)
// before the inverted index is built, so no selector is compiled or
// assigned a new SelectorID after matchedByIdentity has been computed.
// Returned slice is indexed by position in policies.
func registerAllSelectors(policies []repr.NetworkPolicy, registry *repr.SelectorRegistry) ([]repr.SelectorID, error) {
	policySelector := make([]repr.SelectorID, len(policies))
	for i, np := range policies {
		sid, err := registry.Get(np.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", np.Name, err)
		}
		policySelector[i] = sid

		for _, rule := range np.Ingress {
			if err := registerPeers(rule.Peers, registry); err != nil {
				return nil, fmt.Errorf("policy %s ingress rule: %w", np.Name, err)
			}
		}
		for _, rule := range np.Egress {
			if err := registerPeers(rule.Peers, registry); err != nil {
				return nil, fmt.Errorf("policy %s egress rule: %w", np.Name, err)
			}
		}
	}
	return policySelector, nil
}

func registerPeers(peers []repr.Peer, registry *repr.SelectorRegistry) error {
	for _, peer := range peers {
		if peer.PodSelector != nil {
			if _, err := registry.Get(*peer.PodSelector); err != nil {
				return err
			}
		}
	}
	return nil
}

// tupleTemplate is a resolved peer/port/verdict combination for a
// (namespace, identity) group, missing only the enforcing pod's own
// address(es) — supplied later by makeTuples per pod.
type tupleTemplate struct {
	verdict Verdict
	peer    peerEntry
	port    portEntry
}

type groupTemplates struct {
	ingress []tupleTemplate
	egress  []tupleTemplate
}

// ruleRef is one ingress or egress rule's peer/port spec plus enough of its
// owning policy to resolve and error-report it, flattened out of repr.Rule
// so resolveTupleTemplates can handle both directions with one
// implementation.
type ruleRef struct {
	policyName      string
	policyNamespace repr.StringID
	peers           []repr.Peer
	ports           []repr.Port
}

// computeGroupTemplates resolves the ingress/egress rule chain once for
// every pod sharing selfMatch (a (namespace, identity) group), mirroring
// renderPod's logic but replacing selector evaluation with bitmap tests and
// deferring per-pod own-address substitution to the caller.
func computeGroupTemplates(in *Input, selfMatch repr.Bitset, nsPolicyIdx []int, policySelector []repr.SelectorID, registry *repr.SelectorRegistry, matchedByIdentity []repr.Bitset, identitiesByNS map[repr.StringID][]repr.LabelSetID, podsByNS map[repr.StringID]map[repr.LabelSetID][]repr.Pod, nsIndex *repr.NamespaceSelectorCache) (*groupTemplates, error) {
	var selectingIdx []int
	for _, idx := range nsPolicyIdx {
		if selfMatch.Test(uint32(policySelector[idx])) {
			selectingIdx = append(selectingIdx, idx)
		}
	}

	var ingressIdx, egressIdx []int
	for _, idx := range selectingIdx {
		ingress, egress := effectivePolicyTypes(in.Policies[idx])
		if ingress {
			ingressIdx = append(ingressIdx, idx)
		}
		if egress {
			egressIdx = append(egressIdx, idx)
		}
	}

	var ingressRules []ruleRef
	for _, idx := range ingressIdx {
		np := in.Policies[idx]
		for _, rule := range np.Ingress {
			ingressRules = append(ingressRules, ruleRef{np.Name, np.Namespace, rule.Peers, rule.Ports})
		}
	}
	ingress, err := resolveTupleTemplates(len(ingressIdx) > 0, ingressRules, registry, matchedByIdentity, identitiesByNS, podsByNS, nsIndex)
	if err != nil {
		return nil, err
	}

	var egressRules []ruleRef
	for _, idx := range egressIdx {
		np := in.Policies[idx]
		for _, rule := range np.Egress {
			egressRules = append(egressRules, ruleRef{np.Name, np.Namespace, rule.Peers, rule.Ports})
		}
	}
	egress, err := resolveTupleTemplates(len(egressIdx) > 0, egressRules, registry, matchedByIdentity, identitiesByNS, podsByNS, nsIndex)
	if err != nil {
		return nil, err
	}

	return &groupTemplates{ingress: ingress, egress: egress}, nil
}

// resolveTupleTemplates resolves one direction's already-flattened rules
// into its allow entries, appending the trailing default-verdict wildcard:
// Allow if isolated is false (no selecting policy at all for this
// direction), Deny otherwise (isolated, whether by a policy with zero rules
// or by rules that matched no peers).
//
// Peers/ports for every rule are resolved once, up front, which also yields
// the total tupleTemplate count before entries is allocated — instead of
// growing it via append, which at the fan-out a single rule's podSelector
// can reach (thousands of matching pods) means repeated doubling-and-copy
// churn, the same issue RenderIndexed's final tuple slice had (see its doc
// comment).
func resolveTupleTemplates(isolated bool, rules []ruleRef, registry *repr.SelectorRegistry, matchedByIdentity []repr.Bitset, identitiesByNS map[repr.StringID][]repr.LabelSetID, podsByNS map[repr.StringID]map[repr.LabelSetID][]repr.Pod, nsIndex *repr.NamespaceSelectorCache) ([]tupleTemplate, error) {
	if !isolated {
		return []tupleTemplate{{verdict: VerdictAllow, peer: peerEntry{wildcard: true}, port: portEntry{wildcard: true}}}, nil
	}

	type resolvedRule struct {
		peers []peerEntry
		ports []portEntry
	}
	resolved := make([]resolvedRule, len(rules))
	total := 0
	for i, r := range rules {
		peers, err := resolvePeersIndexed(r.peers, r.policyNamespace, registry, matchedByIdentity, identitiesByNS, podsByNS, nsIndex)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", r.policyName, err)
		}
		ports := resolvePorts(r.ports)
		resolved[i] = resolvedRule{peers, ports}
		total += len(peers) * len(ports)
	}

	entries := make([]tupleTemplate, 0, total+1)
	for _, rr := range resolved {
		for _, peer := range rr.peers {
			for _, port := range rr.ports {
				entries = append(entries, tupleTemplate{verdict: VerdictAllow, peer: peer, port: port})
			}
		}
	}
	entries = append(entries, tupleTemplate{verdict: VerdictDeny, peer: peerEntry{wildcard: true}, port: portEntry{wildcard: true}})
	return entries, nil
}

// resolvePeersIndexed is resolvePeers' indexed counterpart: instead of
// scanning every candidate pod in the target namespace(s) and re-evaluating
// a fresh selector against each one, it walks the (much smaller) set of
// distinct identities present per namespace and does an O(1) bitmap test
// per identity, per doc "Label optimizations" #3 and #5.
//
// Known simplification, matching resolvePeers: IPBlock.Except sub-ranges
// are not carved out of the CIDR.
func resolvePeersIndexed(peers []repr.Peer, policyNamespace repr.StringID, registry *repr.SelectorRegistry, matchedByIdentity []repr.Bitset, identitiesByNS map[repr.StringID][]repr.LabelSetID, podsByNS map[repr.StringID]map[repr.LabelSetID][]repr.Pod, nsIndex *repr.NamespaceSelectorCache) ([]peerEntry, error) {
	if len(peers) == 0 {
		return []peerEntry{{wildcard: true}}, nil
	}

	var entries []peerEntry
	for _, peer := range peers {
		if peer.IPBlock != nil {
			entries = append(entries, peerEntry{addr: peer.IPBlock.CIDR.String()})
			continue
		}

		nsIDs := []repr.StringID{policyNamespace}
		if peer.NamespaceSelector != nil {
			ids, err := nsIndex.Match(*peer.NamespaceSelector)
			if err != nil {
				return nil, err
			}
			nsIDs = ids
		}

		var sid repr.SelectorID
		hasSelector := peer.PodSelector != nil
		if hasSelector {
			var err error
			sid, err = registry.Get(*peer.PodSelector)
			if err != nil {
				return nil, err
			}
		}

		for _, ns := range nsIDs {
			for _, id := range identitiesByNS[ns] {
				if hasSelector && !matchedByIdentity[id].Test(uint32(sid)) {
					continue
				}
				for _, pod := range podsByNS[ns][id] {
					for _, ip := range pod.IPs {
						entries = append(entries, peerEntry{addr: ip.String()})
					}
				}
			}
		}
	}
	return entries, nil
}
