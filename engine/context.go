// Package engine contains the pipeline execution core: typed contexts,
// registry-independent operation interfaces, and the pipeline executor.
package engine

import (
	"context"
	"sync"
)

// Command — the run command (what to do with the units).
type Command string

const (
	CommandPlan     Command = "plan"
	CommandApply    Command = "apply"
	CommandValidate Command = "validate"
)

// RunInfo holds the immutable parameters of a run. The affected set
// (ChangedDirs) comes from the outside: the orchestrator or a CLI flag.
type RunInfo struct {
	RunID string
	Repo  string
	PR    int
	SHA   string
	// Command is the terragrunt command (plan/apply/...) the run targets.
	Command Command
	// Args are extra arguments passed through to terragrunt after Command,
	// e.g. everything after `--` on the `oink run` command line.
	Args        []string
	ChangedDirs []string
	RepoRoot    string
}

// RunContext is the run-level context. Only run operations write to Vars:
// the engine is single-threaded between top-level operations.
type RunContext struct {
	Run    RunInfo
	Units  *UnitGraph // nil until discovery; guaranteed by requires/provides
	Events EventSink
	Vars   *Vars
}

// UnitsCount returns the number of units in the graph (0 before discovery).
func (rc *RunContext) UnitsCount() int {
	if rc.Units == nil {
		return 0
	}
	return len(rc.Units.All())
}

// Unit is a single terragrunt unit (directory) with its direct dependencies.
type Unit struct {
	Path string   `json:"path"`
	Deps []string `json:"deps,omitempty"`
	// SourceFiles are files outside the unit directory whose changes should
	// trigger planning this unit: configs the unit reads (includes etc.) plus
	// the files of its local terraform module, recursively over local
	// submodules. Paths are relative to the repo root and may escape it
	// (../...) the same way terragrunt reports them.
	SourceFiles []string `json:"source_files,omitempty"`
}

// UnitContext is the per-unit operation context. Run is read-only by
// convention: unit operations must not write to Run.Vars; unit-local state
// goes to Vars.
type UnitContext struct {
	Run  *RunContext
	Unit *Unit
	Prev *Result // result of the previous unit-pipeline operation, nil for the first
	Vars *Vars   // unit-local, isolated from pool neighbours
}

// RunOperation is a top-level operation, executed once per run.
type RunOperation interface {
	Name() string
	Run(ctx context.Context, rc *RunContext) Result
}

// UnitOperation is an operation inside units.pipeline, executed per unit.
type UnitOperation interface {
	Name() string
	RunUnit(ctx context.Context, uc *UnitContext) Result
}

// Vars is a thread-safe typed store. Type safety is enforced by the phantom
// key type Key[T]; no map[string]any leaks into public APIs.
type Vars struct {
	mu sync.RWMutex
	m  map[string]any
}

func NewVars() *Vars {
	return &Vars{m: make(map[string]any)}
}

// Key is a typed store key. Two keys with the same name but different T are
// distinct keys: Get with a "foreign" type returns ok=false.
type Key[T any] struct{ name string }

func NewKey[T any](name string) Key[T] { return Key[T]{name: name} }

func (k Key[T]) Name() string { return k.name }

func Set[T any](v *Vars, k Key[T], val T) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[k.name] = val
}

func Get[T any](v *Vars, k Key[T]) (T, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	raw, ok := v.m[k.name]
	if !ok {
		var zero T
		return zero, false
	}
	val, ok := raw.(T)
	return val, ok
}
