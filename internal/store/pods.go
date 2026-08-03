package store

import "k8s.io/utils/set"

type Pods struct{}

func NewPods() *Pods { return nil }

// Init is called during startup. This can only be called after the
// relevant keys has been initialized from the NetworkPolicies.
func (pods *Pods) Init(relevantKeys set.Set[string]) error { return nil }
