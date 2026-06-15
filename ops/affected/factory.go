package affected

import (
	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/ops/discovery"
	"github.com/tufitko/oink/registry"
)

// Factory is the registry factory of the affected builtin.
func Factory() registry.Factory {
	return registry.Factory{
		Meta: config.OpMeta{Run: true, Requires: []string{discovery.KeyName}},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			return New(), nil
		},
	}
}
