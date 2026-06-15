package execop

import (
	"fmt"

	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/registry"
)

// FactoryParams are the exec with-parameters in their YAML form.
type FactoryParams struct {
	Run     string          `yaml:"run"` // shell command template to execute
	Timeout config.Duration `yaml:"timeout"`
}

// Factory is the registry factory of the exec builtin.
func Factory() registry.Factory {
	return registry.Factory{
		Meta: config.OpMeta{Run: true, Unit: true},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			var p FactoryParams
			if err := op.DecodeWith(&p); err != nil {
				return nil, err
			}
			if p.Run == "" {
				return nil, fmt.Errorf("exec requires with.run")
			}
			return New(op.DisplayName(), p.Run, p.Timeout.Std()), nil
		},
	}
}
