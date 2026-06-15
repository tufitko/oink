// Package oink is the public facade of the terragrunt GitOps runner.
//
// The primary use case is embedding into Go applications:
//
//	reg := oink.DefaultRegistry()
//	reg.MustRegister("my-op", registry.Factory{...})
//	status, err := oink.Run(ctx, oink.Options{
//	    Run:        engine.RunInfo{RunID: "...", Command: engine.CommandPlan, ...},
//	    ConfigPath: ".tgops.yaml",
//	    Registry:   reg,
//	})
//
// The CLI (cmd/oink) is a thin wrapper over this same API.
package oink

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/engine"
	"github.com/tufitko/oink/events"
	"github.com/tufitko/oink/ops/affected"
	"github.com/tufitko/oink/ops/discovery"
	"github.com/tufitko/oink/ops/execop"
	"github.com/tufitko/oink/ops/report"
	"github.com/tufitko/oink/ops/terragrunt"
	"github.com/tufitko/oink/ops/units"
	"github.com/tufitko/oink/registry"
)

// Options are the pipeline run parameters.
type Options struct {
	// Run holds the immutable run parameters (run id, command, affected set).
	Run engine.RunInfo

	// Config is a pre-loaded repo config. If nil, it is read from ConfigPath.
	Config     *config.Pipeline
	ConfigPath string

	// ServerConfig holds server-side defaults; merged under the repo config.
	// Usually nil at this stage.
	ServerConfig *config.Pipeline

	// Registry is the operation registry. nil → DefaultRegistry() (builtins).
	Registry *registry.Registry

	// Events is the execution event sink. nil → NDJSON to Stdout.
	Events engine.EventSink

	// Stdout is the human-readable operation output (report) and the default
	// NDJSON event destination. nil → os.Stdout.
	Stdout io.Writer

	// Logger receives operation execution logs (start/finish, status,
	// duration). Every operation is additionally wrapped with pprof labels
	// (pprof.Do) and a trace region (trace.WithRegion) — those are active
	// whenever profiling/tracing is on (see the diag package). nil → logging
	// disabled.
	Logger *slog.Logger
}

// DefaultRegistry returns a registry with the stage-1 builtins:
// discovery, affected, units, exec, terragrunt, report.
func DefaultRegistry() *registry.Registry {
	r := registry.New()
	r.MustRegister("discovery", discovery.Factory())
	r.MustRegister("affected", affected.Factory())
	r.MustRegister("units", units.Factory())
	r.MustRegister("exec", execop.Factory())
	r.MustRegister("terragrunt", terragrunt.Factory())
	r.MustRegister("report", report.Factory())
	return r
}

// Run loads, validates and executes the pipeline.
// An error is returned only for configuration problems (load/merge/validate/build);
// operation failures are reflected in the returned status.
func Run(ctx context.Context, opts Options) (engine.Status, error) {
	cfg := opts.Config
	if cfg == nil {
		if opts.ConfigPath == "" {
			return engine.StatusFailed, fmt.Errorf("either Config or ConfigPath is required")
		}
		loaded, err := config.Load(opts.ConfigPath)
		if err != nil {
			return engine.StatusFailed, err
		}
		cfg = loaded
	}
	cfg = config.Merge(opts.ServerConfig, cfg)

	reg := opts.Registry
	if reg == nil {
		reg = DefaultRegistry()
	}
	if err := config.Validate(cfg, reg); err != nil {
		return engine.StatusFailed, fmt.Errorf("invalid pipeline config: %w", err)
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	plan, err := registry.Build(reg, cfg, registry.BuildContext{Registry: reg, Stdout: stdout, Logger: opts.Logger})
	if err != nil {
		return engine.StatusFailed, fmt.Errorf("build pipeline: %w", err)
	}

	sink := opts.Events
	if sink == nil {
		sink = events.NewNDJSON(stdout)
	}
	rc := &engine.RunContext{
		Run:    opts.Run,
		Events: sink,
		Vars:   engine.NewVars(),
	}
	return engine.Execute(ctx, rc, plan), nil
}
