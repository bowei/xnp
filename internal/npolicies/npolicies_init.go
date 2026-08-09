package npolicies

import v1 "k8s.io/api/networking/v1"

func (nps *NPolicies) Init(
	npSeq func(yield func(*v1.NetworkPolicy) bool),
) {

}
