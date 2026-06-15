package config

import (
	"fmt"
)

// OpMeta is the static operation metadata declared by its registry factory.
// Used by the validator before operations are instantiated.
type OpMeta struct {
	Requires []string // context keys that must be provided earlier
	Provides []string // context keys the operation sets
	Run      bool     // implements RunOperation (allowed at the top level)
	Unit     bool     // implements UnitOperation (allowed inside units.pipeline)
	// UnitPipeline marks an operation carrying a nested unit pipeline in
	// with.pipeline (builtin units). The validator checks it recursively.
	UnitPipeline bool
}

// Registry is what the validator needs to know about the operation registry.
type Registry interface {
	Meta(use string) (OpMeta, bool)
}

// OnFailure is the operation failure policy. Empty means OnFailureTerminate.
type OnFailure string

const (
	OnFailureTerminate OnFailure = "terminate"
	OnFailureContinue  OnFailure = "continue"
)

// Validate checks the config before execution. Errors include the path to
// the offending operation (e.g. pipeline[2].with.pipeline[0]).
func Validate(p *Pipeline, reg Registry) error {
	provided := map[string]bool{}
	if err := validateList(p.Pipeline, "pipeline", reg, provided, false); err != nil {
		return err
	}
	// finally is validated against the provides of the whole main pipeline:
	// statically it runs after it.
	return validateList(p.Finally, "finally", reg, provided, false)
}

func validateList(ops []Operation, path string, reg Registry, provided map[string]bool, unitLevel bool) error {
	for i, op := range ops {
		opPath := fmt.Sprintf("%s[%d]", path, i)
		if err := validateOp(op, opPath, reg, provided, unitLevel); err != nil {
			return err
		}
	}
	return nil
}

func validateOp(op Operation, path string, reg Registry, provided map[string]bool, unitLevel bool) error {
	if op.Use == "" {
		return fmt.Errorf("%s: 'use' is required", path)
	}
	switch op.OnFailure {
	case "", OnFailureTerminate, OnFailureContinue:
	default:
		return fmt.Errorf("%s: invalid on_failure %q (want %q or %q)", path, op.OnFailure, OnFailureTerminate, OnFailureContinue)
	}

	use := op.Use
	meta, ok := reg.Meta(use)
	if !ok {
		return fmt.Errorf("%s: unknown operation %q", path, use)
	}
	if unitLevel && !meta.Unit {
		return fmt.Errorf("%s: operation %q cannot be used inside units.pipeline (does not implement UnitOperation)", path, use)
	}
	if !unitLevel && !meta.Run {
		return fmt.Errorf("%s: operation %q cannot be used at the top level (does not implement RunOperation)", path, use)
	}

	if !unitLevel {
		for _, req := range meta.Requires {
			if !provided[req] {
				return fmt.Errorf("%s: operation %q requires context key %q, which is not provided by any preceding operation", path, use, req)
			}
		}
		for _, prov := range meta.Provides {
			provided[prov] = true
		}
	}

	if meta.UnitPipeline {
		nested, err := op.UnitPipeline()
		if err != nil {
			return fmt.Errorf("%s: decode with.pipeline: %w", path, err)
		}
		if len(nested) == 0 {
			return fmt.Errorf("%s: operation %q requires a non-empty with.pipeline", path, use)
		}
		if err := validateList(nested, path+".with.pipeline", reg, provided, true); err != nil {
			return err
		}
	}
	return nil
}
