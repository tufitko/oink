package engine

import (
	"context"
	"sync"
	"testing"
)

// memSink collects events in memory.
type memSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *memSink) Emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// fakeOp is a controllable run operation for tests.
type fakeOp struct {
	name   string
	result Result
	panics bool
	calls  *[]string
}

func (f *fakeOp) Name() string { return f.name }

func (f *fakeOp) Run(ctx context.Context, rc *RunContext) Result {
	if f.calls != nil {
		*f.calls = append(*f.calls, f.name)
	}
	if f.panics {
		panic("boom")
	}
	return f.result
}

func newRC(sink EventSink) *RunContext {
	return &RunContext{
		Run:    RunInfo{RunID: "test-run"},
		Events: sink,
		Vars:   NewVars(),
	}
}

func TestExecuteTerminateSkipsRestRunsFinally(t *testing.T) {
	var calls []string
	plan := &Plan{
		Pipeline: []Step{
			{Op: &fakeOp{name: "a", calls: &calls}},
			{Op: &fakeOp{name: "b", calls: &calls, result: Result{Status: StatusFailed}}, OnFailure: OnFailureTerminate},
			{Op: &fakeOp{name: "c", calls: &calls}},
		},
		Finally: []Step{
			{Op: &fakeOp{name: "fin", calls: &calls}},
		},
	}
	status := Execute(context.Background(), newRC(NopSink{}), plan)
	if status != StatusFailed {
		t.Fatalf("status = %v, want failed", status)
	}
	want := []string{"a", "b", "fin"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}

func TestExecuteContinueGivesWarn(t *testing.T) {
	var calls []string
	plan := &Plan{
		Pipeline: []Step{
			{Op: &fakeOp{name: "a", calls: &calls, result: Result{Status: StatusFailed}}, OnFailure: OnFailureContinue},
			{Op: &fakeOp{name: "b", calls: &calls}},
		},
	}
	status := Execute(context.Background(), newRC(NopSink{}), plan)
	if status != StatusWarn {
		t.Fatalf("status = %v, want warn", status)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want both ops executed", calls)
	}
}

func TestExecutePanicRecoveredFinallyRuns(t *testing.T) {
	var calls []string
	plan := &Plan{
		Pipeline: []Step{
			{Op: &fakeOp{name: "boom", calls: &calls, panics: true}},
			{Op: &fakeOp{name: "after", calls: &calls}},
		},
		Finally: []Step{
			{Op: &fakeOp{name: "fin", calls: &calls}},
		},
	}
	status := Execute(context.Background(), newRC(NopSink{}), plan)
	if status != StatusFailed {
		t.Fatalf("status = %v, want failed", status)
	}
	want := []string{"boom", "fin"}
	if len(calls) != len(want) || calls[0] != "boom" || calls[1] != "fin" {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestExecuteCanceledContextStillRunsFinally(t *testing.T) {
	var calls []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := &Plan{
		Pipeline: []Step{{Op: &fakeOp{name: "a", calls: &calls}}},
		Finally:  []Step{{Op: &fakeOp{name: "fin", calls: &calls}}},
	}
	status := Execute(ctx, newRC(NopSink{}), plan)
	if status != StatusFailed {
		t.Fatalf("status = %v, want failed", status)
	}
	if len(calls) != 1 || calls[0] != "fin" {
		t.Fatalf("calls = %v, want only fin", calls)
	}
}

func TestExecuteEventOrder(t *testing.T) {
	sink := &memSink{}
	plan := &Plan{
		Pipeline: []Step{
			{Op: &fakeOp{name: "a"}},
			{Op: &fakeOp{name: "b", result: Result{Status: StatusFailed}}, OnFailure: OnFailureContinue},
		},
		Finally: []Step{{Op: &fakeOp{name: "fin"}}},
	}
	Execute(context.Background(), newRC(sink), plan)

	type ev struct {
		typ    EventType
		op     string
		status string
	}
	want := []ev{
		{EventStarted, "a", ""},
		{EventFinished, "a", "ok"},
		{EventStarted, "b", ""},
		{EventFinished, "b", "failed"},
		{EventStarted, "fin", ""},
		{EventFinished, "fin", "ok"},
	}
	if len(sink.events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(sink.events), len(want), sink.events)
	}
	for i, w := range want {
		got := sink.events[i]
		if got.Type != w.typ || got.Op != w.op || got.Status != w.status {
			t.Errorf("event %d = {%s %s %s}, want {%s %s %s}", i, got.Type, got.Op, got.Status, w.typ, w.op, w.status)
		}
		if got.RunID != "test-run" {
			t.Errorf("event %d run_id = %q", i, got.RunID)
		}
	}
}
