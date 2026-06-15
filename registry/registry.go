// Package registry — the operation registry and compilation of the config
// into an engine.Plan. The registry is instance-based: an embedding
// application creates its own registry, registers custom operations next to
// the builtins, and runs the pipeline.
package registry

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/engine"
)

// BuildContext is the environment operations are compiled in.
type BuildContext struct {
	Registry *Registry
	Stdout   io.Writer // human-readable operation output (report); nil → os.Stdout
	// Logger receives operation execution logs (start/finish, status,
	// duration). nil → logging disabled; pprof labels and trace regions are
	// applied regardless.
	Logger *slog.Logger
}

// Factory creates an operation instance from its config. The returned value
// must implement engine.RunOperation and/or engine.UnitOperation — matching
// the declared Meta.Run/Meta.Unit.
type Factory struct {
	Meta config.OpMeta
	New  func(bc BuildContext, op config.Operation) (any, error)
}

// Registry maps use names to operation factories.
type Registry struct {
	factories map[string]Factory
}

func New() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds a factory. Registering the same name twice is an error.
func (r *Registry) Register(name string, f Factory) error {
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("operation %q is already registered", name)
	}
	if f.New == nil {
		return fmt.Errorf("operation %q: factory New is nil", name)
	}
	r.factories[name] = f
	return nil
}

// MustRegister is Register that panics; for static registry initialization.
func (r *Registry) MustRegister(name string, f Factory) {
	if err := r.Register(name, f); err != nil {
		panic(err)
	}
}

// Meta implements config.Registry for the validator.
func (r *Registry) Meta(use string) (config.OpMeta, bool) {
	f, ok := r.factories[use]
	return f.Meta, ok
}

func (r *Registry) factory(use string) (Factory, bool) {
	f, ok := r.factories[use]
	return f, ok
}

// Build compiles a validated config into an executable plan.
// Assumes config.Validate has already passed.
func Build(reg *Registry, cfg *config.Pipeline, bc BuildContext) (*engine.Plan, error) {
	if bc.Registry == nil {
		bc.Registry = reg
	}
	pipeline, err := buildSteps(reg, cfg.Pipeline, "pipeline", bc)
	if err != nil {
		return nil, err
	}
	finally, err := buildSteps(reg, cfg.Finally, "finally", bc)
	if err != nil {
		return nil, err
	}
	return &engine.Plan{Pipeline: pipeline, Finally: finally}, nil
}

func buildSteps(reg *Registry, ops []config.Operation, path string, bc BuildContext) ([]engine.Step, error) {
	var steps []engine.Step
	for i, op := range ops {
		opPath := fmt.Sprintf("%s[%d]", path, i)
		inst, err := instantiate(reg, op, opPath, bc)
		if err != nil {
			return nil, err
		}
		runOp, ok := inst.(engine.RunOperation)
		if !ok {
			return nil, fmt.Errorf("%s: operation %q does not implement RunOperation", opPath, op.Use)
		}
		steps = append(steps, engine.Step{Op: engine.InstrumentRun(runOp, bc.Logger), OnFailure: onFailure(op)})
	}
	return steps, nil
}

// BuildUnitSteps compiles a nested unit pipeline. Used by the units builtin
// factory (and by any custom operation carrying its own unit pipeline).
func BuildUnitSteps(bc BuildContext, ops []config.Operation) ([]engine.UnitStep, error) {
	var steps []engine.UnitStep
	for i, op := range ops {
		opPath := fmt.Sprintf("with.pipeline[%d]", i)
		inst, err := instantiate(bc.Registry, op, opPath, bc)
		if err != nil {
			return nil, err
		}
		unitOp, ok := inst.(engine.UnitOperation)
		if !ok {
			return nil, fmt.Errorf("%s: operation %q does not implement UnitOperation", opPath, op.Use)
		}
		steps = append(steps, engine.UnitStep{Op: engine.InstrumentUnit(unitOp, bc.Logger), OnFailure: onFailure(op)})
	}
	return steps, nil
}

func instantiate(reg *Registry, op config.Operation, opPath string, bc BuildContext) (any, error) {
	f, ok := reg.factory(op.Use)
	if !ok {
		return nil, fmt.Errorf("%s: unknown operation %q", opPath, op.Use)
	}
	inst, err := f.New(bc, op)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", opPath, err)
	}
	return inst, nil
}

func onFailure(op config.Operation) engine.OnFailure {
	if op.OnFailure == config.OnFailureContinue {
		return engine.OnFailureContinue
	}
	return engine.OnFailureTerminate
}
