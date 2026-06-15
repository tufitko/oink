package units

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tufitko/oink/engine"
)

// stubOp is a controllable unit operation.
type stubOp struct {
	name    string
	failFor map[string]bool // unit path → return Failed
	delay   time.Duration

	mu         sync.Mutex
	order      []string // launch order (unit paths)
	current    int64
	maxCurrent int64

	barrier chan struct{} // if set, the operation waits until it is closed
	started *sync.WaitGroup
}

func (s *stubOp) Name() string { return s.name }

func (s *stubOp) RunUnit(ctx context.Context, uc *engine.UnitContext) engine.Result {
	cur := atomic.AddInt64(&s.current, 1)
	defer atomic.AddInt64(&s.current, -1)
	for {
		max := atomic.LoadInt64(&s.maxCurrent)
		if cur <= max || atomic.CompareAndSwapInt64(&s.maxCurrent, max, cur) {
			break
		}
	}

	s.mu.Lock()
	s.order = append(s.order, uc.Unit.Path)
	s.mu.Unlock()

	if s.started != nil {
		s.started.Done()
	}
	if s.barrier != nil {
		select {
		case <-s.barrier:
		case <-time.After(5 * time.Second):
			return engine.Result{Status: engine.StatusFailed}
		}
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.failFor[uc.Unit.Path] {
		return engine.Result{Status: engine.StatusFailed, ExitCode: 1}
	}
	return engine.Result{Status: engine.StatusOK}
}

func testGraph(t *testing.T) *engine.UnitGraph {
	t.Helper()
	g, err := engine.NewUnitGraph([]*engine.Unit{
		{Path: "net"},
		{Path: "db", Deps: []string{"net"}},
		{Path: "app", Deps: []string{"db"}},
		{Path: "worker", Deps: []string{"db"}},
		{Path: "standalone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func newRC(t *testing.T) *engine.RunContext {
	return &engine.RunContext{
		Run:    engine.RunInfo{RunID: "r1"},
		Units:  testGraph(t),
		Events: engine.NopSink{},
		Vars:   engine.NewVars(),
	}
}

func steps(ops ...engine.UnitOperation) []engine.UnitStep {
	out := make([]engine.UnitStep, len(ops))
	for i, op := range ops {
		out[i] = engine.UnitStep{Op: op, OnFailure: engine.OnFailureTerminate}
	}
	return out
}

func TestOrderNoneFullParallelism(t *testing.T) {
	// All 5 units must start concurrently: each blocks on a barrier that is
	// closed only once all of them have started.
	var started sync.WaitGroup
	started.Add(5)
	barrier := make(chan struct{})
	stub := &stubOp{name: "work", barrier: barrier, started: &started}

	op, err := New(5, OrderNone, steps(stub))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan engine.Result, 1)
	rc := newRC(t)
	go func() { done <- op.Run(context.Background(), rc) }()

	waitCh := make(chan struct{})
	go func() { started.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(3 * time.Second):
		t.Fatal("not all units started concurrently in order=none")
	}
	close(barrier)

	res := <-done
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	if got := atomic.LoadInt64(&stub.maxCurrent); got != 5 {
		t.Fatalf("max concurrency = %d, want 5", got)
	}
}

func TestOrderDAGTopologicalWaves(t *testing.T) {
	stub := &stubOp{name: "work"}
	op, err := New(8, OrderDAG, steps(stub))
	if err != nil {
		t.Fatal(err)
	}
	res := op.Run(context.Background(), newRC(t))
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}

	idx := func(path string) int { return slices.Index(stub.order, path) }
	if idx("net") > idx("db") || idx("db") > idx("app") || idx("db") > idx("worker") {
		t.Fatalf("dag order violated: %v", stub.order)
	}
}

func TestFailedCascadesToSkipped(t *testing.T) {
	stub := &stubOp{name: "work", failFor: map[string]bool{"db": true}}
	op, err := New(8, OrderDAG, steps(stub))
	if err != nil {
		t.Fatal(err)
	}
	rc := newRC(t)
	res := op.Run(context.Background(), rc)
	if res.Status != engine.StatusFailed {
		t.Fatalf("status = %v, want failed", res.Status)
	}

	results, ok := engine.Get(rc.Vars, KeyResults)
	if !ok {
		t.Fatal("unit_results not provided")
	}
	want := map[string]engine.Status{
		"net":        engine.StatusOK,
		"db":         engine.StatusFailed,
		"app":        engine.StatusSkipped,
		"worker":     engine.StatusSkipped,
		"standalone": engine.StatusOK,
	}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d", len(results), len(want))
	}
	for path, status := range want {
		ur := results[path]
		if ur == nil {
			t.Fatalf("no result for %s", path)
		}
		if ur.Status != status {
			t.Errorf("%s: status = %v, want %v", path, ur.Status, status)
		}
	}
}

func TestParallelismLimit(t *testing.T) {
	stub := &stubOp{name: "work", delay: 50 * time.Millisecond}
	op, err := New(2, OrderNone, steps(stub))
	if err != nil {
		t.Fatal(err)
	}
	res := op.Run(context.Background(), newRC(t))
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	if got := atomic.LoadInt64(&stub.maxCurrent); got > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", got)
	}
}

func TestUnitPipelinePrevAndOnFailure(t *testing.T) {
	// The first operation fails with continue → the unit is WARN and the
	// second operation sees Prev.
	first := &failingOp{name: "pre", exitCode: 7}
	var prevExit int
	second := &captureOp{name: "after", capture: func(uc *engine.UnitContext) {
		if uc.Prev != nil {
			prevExit = uc.Prev.ExitCode
		}
	}}

	op, err := New(1, OrderNone, []engine.UnitStep{
		{Op: first, OnFailure: engine.OnFailureContinue},
		{Op: second, OnFailure: engine.OnFailureTerminate},
	})
	if err != nil {
		t.Fatal(err)
	}

	rc := newRC(t)
	g, _ := engine.NewUnitGraph([]*engine.Unit{{Path: "only"}})
	rc.Units = g

	res := op.Run(context.Background(), rc)
	if res.Status != engine.StatusWarn {
		t.Fatalf("status = %v, want warn", res.Status)
	}
	results, _ := engine.Get(rc.Vars, KeyResults)
	if results["only"].Status != engine.StatusWarn {
		t.Fatalf("unit status = %v, want warn", results["only"].Status)
	}
	if len(results["only"].Ops) != 2 {
		t.Fatalf("ops recorded = %d, want 2 (continue must not stop the unit)", len(results["only"].Ops))
	}
	if prevExit != 7 {
		t.Fatalf("Prev.ExitCode = %d, want 7", prevExit)
	}
}

func TestUnitTerminateStopsUnitPipeline(t *testing.T) {
	first := &failingOp{name: "pre", exitCode: 1}
	second := &captureOp{name: "after"}

	op, err := New(1, OrderNone, []engine.UnitStep{
		{Op: first, OnFailure: engine.OnFailureTerminate},
		{Op: second, OnFailure: engine.OnFailureTerminate},
	})
	if err != nil {
		t.Fatal(err)
	}
	rc := newRC(t)
	g, _ := engine.NewUnitGraph([]*engine.Unit{{Path: "only"}})
	rc.Units = g

	res := op.Run(context.Background(), rc)
	if res.Status != engine.StatusFailed {
		t.Fatalf("status = %v, want failed", res.Status)
	}
	results, _ := engine.Get(rc.Vars, KeyResults)
	if got := len(results["only"].Ops); got != 1 {
		t.Fatalf("ops recorded = %d, want 1 (terminate must stop the unit)", got)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(1, "weird", steps(&stubOp{name: "x"})); err == nil {
		t.Fatal("want error for invalid order")
	}
	if _, err := New(1, OrderNone, nil); err == nil {
		t.Fatal("want error for empty pipeline")
	}
}

type failingOp struct {
	name     string
	exitCode int
}

func (f *failingOp) Name() string { return f.name }
func (f *failingOp) RunUnit(ctx context.Context, uc *engine.UnitContext) engine.Result {
	return engine.Result{Status: engine.StatusFailed, ExitCode: f.exitCode}
}

type captureOp struct {
	name    string
	capture func(*engine.UnitContext)
}

func (c *captureOp) Name() string { return c.name }
func (c *captureOp) RunUnit(ctx context.Context, uc *engine.UnitContext) engine.Result {
	if c.capture != nil {
		c.capture(uc)
	}
	return engine.Result{Status: engine.StatusOK}
}
