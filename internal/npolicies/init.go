package npolicies

import (
	"fmt"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/convert"
	"github.com/bowei/xnp/internal/rlabels"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
)

// Init is the startup path (doc/workflow.tex §Startup 2-3): it replaces
// whatever the store holds with the converted contents of a synced
// NetworkPolicy cache, and incremental updates take over from there.
//
// A policy that cannot be converted -- a named port, an unsupported selector
// operator, an unparseable CIDR -- fails the whole call rather than being
// skipped, since silently dropping a policy renders as more allowed traffic
// than the cluster asked for. Both maps are built fresh and committed to the
// receiver only on success, so a failed Init leaves the store exactly as it
// was rather than holding half a cluster.
//
// namespaces is the synced Namespace cache, keyed by name. It is read but not
// retained: a peer's namespaceSelector has to be resolved to say which
// namespaces the peer's podSelector keys are relevant in, and that resolution
// needs the Namespace labels. The relevant labels therefore depend on Namespace
// state as well as policy state, so the incremental path has to recompute them
// when a namespace's labels change or a namespace comes or goes, not only when
// a policy does.
func (nps *Store) Init(
	namespaces map[string]*corev1.Namespace,
	npSeq func(yield func(*v1.NetworkPolicy) bool),
) error {
	nps.lock.Lock()
	defer nps.lock.Unlock()

	byNs := map[string]map[string]*Entry{}
	var rl rlabels.Builder

	// Compiled once for the whole call rather than once per namespaceSelector:
	// every policy that carries one matches against the same namespaces. The
	// full label set is compiled, not a projection onto the relevant keys --
	// which keys are relevant to a Namespace is exactly what is being computed
	// here, and matching the full set is always correct.
	nsLabels := make(map[string]*kv.KV, len(namespaces))
	for _, ns := range namespaces {
		labels := convert.LabelsToKV(ns.Labels)
		nsLabels[ns.Name] = &labels
	}

	var err error
	for np := range npSeq {
		var e *Entry
		e, err = newEntry(np)
		if err != nil {
			// The iterator is stopped rather than returned from directly: a
			// range-over-func body cannot return for its caller, and running it
			// to completion after a failure would convert policies whose
			// results are already known to be discarded.
			break
		}

		// ns -> name -> entry.
		inNs, ok := byNs[e.Namespace]
		if !ok {
			inNs = map[string]*Entry{}
			byNs[e.Namespace] = inNs
		}
		inNs[e.Name] = e

		e.mergeKeys(&rl, np, nsLabels)
	}
	if err != nil {
		return fmt.Errorf("NPolicies.Init: %w", err)
	}

	nps.nps = byNs
	nps.rlabels = rl.Build()

	return nil
}
