package engine

import (
	"context"
	"fmt"
	"time"
)

// OnFailure defines behaviour when an operation fails.
type OnFailure string

const (
	OnFailureTerminate OnFailure = "terminate" // default
	OnFailureContinue  OnFailure = "continue"
)

// Step is a compiled top-level operation.
type Step struct {
	Op        RunOperation
	OnFailure OnFailure
}

// UnitStep is a compiled unit-pipeline operation. It is executed by the
// units scheduler; the engine knows nothing about it.
type UnitStep struct {
	Op        UnitOperation
	OnFailure OnFailure
}

// Plan is a compiled pipeline ready for execution.
type Plan struct {
	Pipeline []Step
	Finally  []Step
}

// Execute runs the pipeline. Semantics:
//   - on_failure: terminate → the rest of the pipeline is skipped, control moves to finally;
//   - on_failure: continue → the result becomes WARN, execution continues;
//   - finally always runs, including after a panic (recover) and context cancellation.
func Execute(ctx context.Context, rc *RunContext, p *Plan) Status {
	overall := StatusOK

	func() {
		defer recoverToStatus(rc, &overall)
		for _, step := range p.Pipeline {
			if ctx.Err() != nil {
				overall = overall.Worse(StatusFailed)
				return
			}
			status := runStep(ctx, rc, step, "")
			overall = overall.Worse(status)
			if status == StatusFailed {
				return // terminate: skip the rest, finally still runs
			}
		}
	}()

	// finally runs even after an abort — on its own context.
	finallyCtx := context.WithoutCancel(ctx)
	func() {
		defer recoverToStatus(rc, &overall)
		for _, step := range p.Finally {
			status := runStep(finallyCtx, rc, step, "")
			overall = overall.Worse(status)
		}
	}()

	return overall
}

// runStep executes one run operation: events, recover, on_failure.
// The returned status already accounts for on_failure (Failed+continue → Warn).
func runStep(ctx context.Context, rc *RunContext, step Step, unit string) Status {
	res := RunGuarded(ctx, rc, step.Op, unit)
	return ApplyOnFailure(res.Status, step.OnFailure)
}

// RunGuarded executes a run operation with event emission and panic recovery.
func RunGuarded(ctx context.Context, rc *RunContext, op RunOperation, unit string) (res Result) {
	started := time.Now()
	rc.Events.Emit(Event{Type: EventStarted, Time: started, RunID: rc.Run.RunID, Op: op.Name(), Unit: unit})
	defer func() {
		if r := recover(); r != nil {
			res = Result{Status: StatusFailed, Err: fmt.Errorf("panic in operation %q: %v", op.Name(), r)}
		}
		emitFinished(rc, op.Name(), unit, started, res)
	}()
	res = op.Run(ctx, rc)
	return res
}

// RunUnitGuarded is the same for a unit operation; used by the units scheduler.
func RunUnitGuarded(ctx context.Context, uc *UnitContext, op UnitOperation) (res Result) {
	rc := uc.Run
	started := time.Now()
	rc.Events.Emit(Event{Type: EventStarted, Time: started, RunID: rc.Run.RunID, Op: op.Name(), Unit: uc.Unit.Path})
	defer func() {
		if r := recover(); r != nil {
			res = Result{Status: StatusFailed, Err: fmt.Errorf("panic in operation %q: %v", op.Name(), r)}
		}
		emitFinished(rc, op.Name(), uc.Unit.Path, started, res)
	}()
	res = op.RunUnit(ctx, uc)
	return res
}

// ApplyOnFailure degrades Failed to Warn under on_failure: continue.
func ApplyOnFailure(s Status, mode OnFailure) Status {
	if s == StatusFailed && mode == OnFailureContinue {
		return StatusWarn
	}
	return s
}

func emitFinished(rc *RunContext, op, unit string, started time.Time, res Result) {
	e := Event{
		Type:       EventFinished,
		Time:       time.Now(),
		RunID:      rc.Run.RunID,
		Op:         op,
		Unit:       unit,
		Status:     res.Status.String(),
		DurationMs: time.Since(started).Milliseconds(),
	}
	if res.Err != nil {
		e.Error = res.Err.Error()
	}
	if out := res.Stdout; out != "" {
		if len(out) > MaxEventOutput {
			out = out[:MaxEventOutput] + "\n...[truncated]"
		}
		e.Output = out
	}
	rc.Events.Emit(e)
}

func recoverToStatus(rc *RunContext, overall *Status) {
	if r := recover(); r != nil {
		*overall = overall.Worse(StatusFailed)
		rc.Events.Emit(Event{
			Type:   EventFinished,
			Time:   time.Now(),
			RunID:  rc.Run.RunID,
			Op:     "engine",
			Status: StatusFailed.String(),
			Error:  fmt.Sprintf("panic: %v", r),
		})
	}
}
