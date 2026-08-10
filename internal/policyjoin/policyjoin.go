package policyjoin

import (
	"github.com/bowei/xnp/internal/npolicies"
	"github.com/bowei/xnp/internal/pods"
)

type entry struct {
	policy *npolicies.Entry
	src
}

type Join struct {
}

func (j *Join) Init(
	np *npolicies.Store,
	pods *pods.Store,
) {

}
