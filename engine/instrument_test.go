package engine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime/pprof"
	"strings"
	"testing"
)

type labelProbeOp struct {
	result Result
	opSeen string
}

func (p *labelProbeOp) Name() string { return "probe" }

func (p *labelProbeOp) Run(ctx context.Context, rc *RunContext) Result {
	p.opSeen, _ = pprof.Label(ctx, "op")
	return p.result
}

type unitLabelProbeOp struct {
	opSeen   string
	unitSeen string
}

func (p *unitLabelProbeOp) Name() string { return "uprobe" }

func (p *unitLabelProbeOp) RunUnit(ctx context.Context, uc *UnitContext) Result {
	p.opSeen, _ = pprof.Label(ctx, "op")
	p.unitSeen, _ = pprof.Label(ctx, "unit")
	return Result{Status: StatusOK}
}

func TestInstrumentRunPassthroughAndLabels(t *testing.T) {
	inner := &labelProbeOp{result: Result{Status: StatusWarn, ExitCode: 3}}
	wrapped := InstrumentRun(inner, nil)

	if wrapped.Name() != "probe" {
		t.Fatalf("Name = %q, want probe", wrapped.Name())
	}
	res := wrapped.Run(context.Background(), newRC(NopSink{}))
	if res.Status != StatusWarn || res.ExitCode != 3 {
		t.Fatalf("result not passed through: %+v", res)
	}
	if inner.opSeen != "probe" {
		t.Fatalf("pprof label op = %q, want probe", inner.opSeen)
	}
}

func TestInstrumentUnitLabels(t *testing.T) {
	inner := &unitLabelProbeOp{}
	wrapped := InstrumentUnit(inner, nil)

	uc := &UnitContext{
		Run:  newRC(NopSink{}),
		Unit: &Unit{Path: "envs/db"},
		Vars: NewVars(),
	}
	res := wrapped.RunUnit(context.Background(), uc)
	if res.Status != StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	if inner.opSeen != "uprobe" || inner.unitSeen != "envs/db" {
		t.Fatalf("pprof labels = (%q, %q), want (uprobe, envs/db)", inner.opSeen, inner.unitSeen)
	}
}

func TestInstrumentRunLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	op := &fakeOp{name: "boom-op", result: Result{Status: StatusFailed, ExitCode: 1, Err: errors.New("boom")}}
	InstrumentRun(op, logger).Run(context.Background(), newRC(NopSink{}))

	out := buf.String()
	for _, want := range []string{"operation started", "operation finished", "op=boom-op", "status=failed", "exit_code=1", "error=boom", "level=ERROR", "run_id=test-run"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q:\n%s", want, out)
		}
	}
}

func TestInstrumentUnitLogsUnitPath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	uc := &UnitContext{Run: newRC(NopSink{}), Unit: &Unit{Path: "envs/db"}, Vars: NewVars()}
	InstrumentUnit(&unitLabelProbeOp{}, logger).RunUnit(context.Background(), uc)

	out := buf.String()
	for _, want := range []string{"unit=envs/db", "op=uprobe", "status=ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q:\n%s", want, out)
		}
	}
}
