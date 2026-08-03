package init

import (
	"fmt"

	"github.com/bowei/xnp/internal/store"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
)

// Caches are the synced Kubernetes object caches Do reads the cluster from
// (doc/workflow.tex §Startup 1, which the caller has already done). Each is an
// iterator over the cache's current contents, in the form the stores' Init
// takes; the order is unspecified and none may be nil.
//
// The cache contents are read once and converted into the stores, so nothing
// here is retained: the API objects are meant to be dropped once repr has
// copied out what policy resolution needs.
type Caches struct {
	NetworkPolicies func(yield func(*v1.NetworkPolicy) bool)
	Pods            func(yield func(*corev1.Pod) bool)
	Namespaces      func(yield func(*corev1.Namespace) bool)
}

// Do the init flow described by workflow.tex. Take in Kubernetes resource
// stores for the relevant Kinds.
//
// The order is forced rather than stylistic. NetworkPolicies goes first because
// its Init is also §Startup 2, extracting the relevant label keys, and Pods.Init
// groups a pod by its labels projected onto that key set — a projection that
// cannot be built before the set is known. Namespaces depends on neither and
// goes last.
//
// §Startup 4 — resolving policies against pods per namespace into the resolved
// policy cache — is not done here because there is no renderer yet. Once there
// is one it belongs at the end of this function, where the three stores are
// consistent with each other and nothing is watching them yet.
//
// A failure aborts the remaining steps and leaves the stores it had not reached
// untouched, so the set as a whole is not a usable view of the cluster: the
// caller must fail startup rather than run the controller on it. Individual
// Inits are still all-or-nothing, so no store is left holding half a cluster.
func Do(stores store.Stores, caches Caches) error {
	if err := stores.NetworkPolicies.Init(caches.NetworkPolicies); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := stores.Pods.Init(stores.NetworkPolicies.RelevantKeys(), caches.Pods); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := stores.Namespaces.Init(caches.Namespaces); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	return nil
}
