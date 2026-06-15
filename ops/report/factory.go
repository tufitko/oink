package report

import (
	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/registry"
)

// Factory is the registry factory of the report builtin.
func Factory() registry.Factory {
	return registry.Factory{
		Meta: config.OpMeta{Run: true},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			var p Params
			if err := op.DecodeWith(&p); err != nil {
				return nil, err
			}
			return New(p.Path, bc.Stdout), nil
		},
	}
}
