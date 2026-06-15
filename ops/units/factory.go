package units

import (
	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/ops/discovery"
	"github.com/tufitko/oink/registry"
)

// Factory is the registry factory of the units builtin.
func Factory() registry.Factory {
	return registry.Factory{
		Meta: config.OpMeta{
			Run:          true,
			Requires:     []string{discovery.KeyName},
			Provides:     []string{KeyName},
			UnitPipeline: true,
		},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			var p Params
			if err := op.DecodeWith(&p); err != nil {
				return nil, err
			}
			nested, err := op.UnitPipeline()
			if err != nil {
				return nil, err
			}
			steps, err := registry.BuildUnitSteps(bc, nested)
			if err != nil {
				return nil, err
			}
			return New(p.Parallelism, p.Order, steps)
		},
	}
}
