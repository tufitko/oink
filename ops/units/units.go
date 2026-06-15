// Package units — builtin DAG scheduler: runs the nested pipeline for every
// unit through a worker pool. The only component aware of the DAG and the
// SKIPPED cascade; the engine knows nothing about them.
package units

import (
	"context"
	"fmt"
	"sync"

	"github.com/tufitko/oink/engine"
)

const (
	OrderNone = "none" // full parallelism; plan reads the current state of dependencies
	OrderDAG  = "dag"  // strict topology (for apply)

	DefaultParallelism = 4
)

// KeyResults is the aggregated unit results; provides: unit_results.
var KeyResults = engine.NewKey[map[string]*UnitResult]("unit_results")

// KeyName is the key name for requires/provides metadata.
const KeyName = "unit_results"

// UnitResult is the outcome of one unit: aggregated status plus per-operation
// results.
type UnitResult struct {
	Unit   *engine.Unit
	Status engine.Status
	Ops    []OpResult
}

// OpResult is the result of one unit-pipeline operation.
type OpResult struct {
	Name   string
	Result engine.Result
}

// Params are the with-parameters (pipeline is parsed separately at compile time).
type Params struct {
	Parallelism int    `yaml:"parallelism"`
	Order       string `yaml:"order"`
}

type Op struct {
	parallelism int
	order       string
	steps       []engine.UnitStep
}

func New(parallelism int, order string, steps []engine.UnitStep) (*Op, error) {
	if parallelism <= 0 {
		parallelism = DefaultParallelism
	}
	switch order {
	case "":
		order = OrderNone
	case OrderNone, OrderDAG:
	default:
		return nil, fmt.Errorf("invalid order %q (want %q or %q)", order, OrderNone, OrderDAG)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("units requires a non-empty pipeline")
	}
	return &Op{parallelism: parallelism, order: order, steps: steps}, nil
}

func (o *Op) Name() string { return "units" }

func (o *Op) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	g := rc.Units
	if g == nil {
		return engine.Result{Status: engine.StatusFailed, Err: fmt.Errorf("unit graph is not in context: discovery must run before units")}
	}

	var batches [][]*engine.Unit
	if o.order == OrderDAG {
		var err error
		batches, err = g.TopoBatches()
		if err != nil {
			return engine.Result{Status: engine.StatusFailed, Err: err}
		}
	} else {
		batches = [][]*engine.Unit{g.All()}
	}

	var (
		mu      sync.Mutex
		skipped = map[string]bool{}
		results = make(map[string]*UnitResult, len(g.All()))
	)
	sem := make(chan struct{}, o.parallelism)

	for _, batch := range batches {
		var wg sync.WaitGroup
		for _, u := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				mu.Lock()
				isSkipped := skipped[u.Path]
				mu.Unlock()
				if isSkipped || ctx.Err() != nil {
					mu.Lock()
					results[u.Path] = &UnitResult{Unit: u, Status: engine.StatusSkipped}
					mu.Unlock()
					return
				}

				ur := o.runUnit(ctx, rc, u)

				mu.Lock()
				results[u.Path] = ur
				if ur.Status == engine.StatusFailed {
					for _, dep := range g.Dependents(u) {
						skipped[dep.Path] = true
					}
				}
				mu.Unlock()
			}()
		}
		wg.Wait()
	}

	// The single write of the aggregate upwards, after the pool has drained.
	engine.Set(rc.Vars, KeyResults, results)

	overall := engine.StatusOK
	for _, ur := range results {
		switch ur.Status {
		case engine.StatusFailed:
			overall = overall.Worse(engine.StatusFailed)
		case engine.StatusWarn:
			overall = overall.Worse(engine.StatusWarn)
		}
	}
	return engine.Result{Status: overall}
}

// runUnit runs the nested pipeline for a single unit.
// on_failure: terminate inside → the unit is FAILED and the rest of its
// pipeline is skipped; continue → WARN and the unit keeps going.
func (o *Op) runUnit(ctx context.Context, rc *engine.RunContext, u *engine.Unit) *UnitResult {
	uc := &engine.UnitContext{Run: rc, Unit: u, Vars: engine.NewVars()}
	ur := &UnitResult{Unit: u, Status: engine.StatusOK}
	for _, step := range o.steps {
		res := engine.RunUnitGuarded(ctx, uc, step.Op)
		ur.Ops = append(ur.Ops, OpResult{Name: step.Op.Name(), Result: res})
		ur.Status = ur.Status.Worse(engine.ApplyOnFailure(res.Status, step.OnFailure))
		if res.Status == engine.StatusFailed && step.OnFailure != engine.OnFailureContinue {
			break
		}
		prev := res
		uc.Prev = &prev
	}
	return ur
}
