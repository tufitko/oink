package discovery

import (
	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/registry"
)

// Factory is the registry factory of the discovery builtin.
func Factory() registry.Factory {
	return registry.Factory{
		Meta: config.OpMeta{Run: true, Provides: []string{KeyName}},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			return New(), nil
		},
	}
}
