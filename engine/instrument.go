package engine

import (
	"context"
	"log/slog"
	"runtime/pprof"
	"runtime/trace"
	"time"
)

// InstrumentRun wraps a run operation with execution logging, pprof labels
// (pprof.Do) and an execution trace region (trace.WithRegion). Labels and
// regions are no-ops unless profiling/tracing is active, so the wrapper is
// always applied.
func InstrumentRun(op RunOperation, log *slog.Logger) RunOperation {
	return &instrumentedRun{op: op, log: ensureLogger(log)}
}

// InstrumentUnit is InstrumentRun for unit operations; pprof labels and log
// records additionally carry the unit path.
func InstrumentUnit(op UnitOperation, log *slog.Logger) UnitOperation {
	return &instrumentedUnit{op: op, log: ensureLogger(log)}
}

func ensureLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return log
}

type instrumentedRun struct {
	op  RunOperation
	log *slog.Logger
}

func (w *instrumentedRun) Name() string { return w.op.Name() }

func (w *instrumentedRun) Run(ctx context.Context, rc *RunContext) (res Result) {
	log := w.log.With("op", w.op.Name(), "run_id", rc.Run.RunID)
	log.Debug("operation started")
	started := time.Now()
	trace.WithRegion(ctx, "op:"+w.op.Name(), func() {
		pprof.Do(ctx, pprof.Labels("op", w.op.Name()), func(ctx context.Context) {
			res = w.op.Run(ctx, rc)
		})
	})
	logFinished(log, started, res)
	return res
}

type instrumentedUnit struct {
	op  UnitOperation
	log *slog.Logger
}

func (w *instrumentedUnit) Name() string { return w.op.Name() }

func (w *instrumentedUnit) RunUnit(ctx context.Context, uc *UnitContext) (res Result) {
	log := w.log.With("op", w.op.Name(), "run_id", uc.Run.Run.RunID, "unit", uc.Unit.Path)
	log.Debug("operation started")
	started := time.Now()
	trace.WithRegion(ctx, "op:"+w.op.Name(), func() {
		pprof.Do(ctx, pprof.Labels("op", w.op.Name(), "unit", uc.Unit.Path), func(ctx context.Context) {
			res = w.op.RunUnit(ctx, uc)
		})
	})
	logFinished(log, started, res)
	return res
}

func logFinished(log *slog.Logger, started time.Time, res Result) {
	attrs := []any{
		slog.String("status", res.Status.String()),
		slog.Duration("duration", time.Since(started)),
	}
	if res.ExitCode != 0 {
		attrs = append(attrs, slog.Int("exit_code", res.ExitCode))
	}
	if res.Err != nil {
		attrs = append(attrs, slog.Any("error", res.Err))
	}
	switch res.Status {
	case StatusFailed:
		log.Error("operation finished", attrs...)
	case StatusWarn:
		log.Warn("operation finished", attrs...)
	default:
		log.Info("operation finished", attrs...)
	}
}
