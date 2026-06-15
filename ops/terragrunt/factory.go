package terragrunt

import (
	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/registry"
)

// FactoryParams are the terragrunt with-parameters in their YAML form.
type FactoryParams struct {
	Command string          `yaml:"command"`
	DryRun  bool            `yaml:"dry_run"`
	Timeout config.Duration `yaml:"timeout"`
}

// Factory is the registry factory of the terragrunt builtin.
func Factory() registry.Factory {
	return registry.Factory{
		Meta: config.OpMeta{Unit: true},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			var p FactoryParams
			if err := op.DecodeWith(&p); err != nil {
				return nil, err
			}
			return New(p.Command, p.DryRun, p.Timeout.Std()), nil
		},
	}
}
