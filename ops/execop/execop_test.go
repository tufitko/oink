package execop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tufitko/oink/engine"
)

func newRC() *engine.RunContext {
	return &engine.RunContext{
		Run:    engine.RunInfo{RunID: "r1", Command: engine.CommandPlan},
		Events: engine.NopSink{},
		Vars:   engine.NewVars(),
	}
}

func TestRunTemplateRendering(t *testing.T) {
	rc := newRC()
	g, err := engine.NewUnitGraph([]*engine.Unit{{Path: "a"}, {Path: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	rc.Units = g

	op := New("hello", `echo "run {{ .Run.RunID }}: {{ .UnitsCount }} units affected"`, 0)
	res := op.Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	if want := "run r1: 2 units affected\n"; res.Stdout != want {
		t.Fatalf("stdout = %q, want %q", res.Stdout, want)
	}
}

func TestRunUnitTemplateRendering(t *testing.T) {
	uc := &engine.UnitContext{
		Run:  newRC(),
		Unit: &engine.Unit{Path: "envs/db"},
		Prev: &engine.Result{ExitCode: 3},
		Vars: engine.NewVars(),
	}
	op := New("after", `echo "done {{ .Unit.Path }}, exit={{ .Prev.ExitCode }}"`, 0)
	res := op.RunUnit(context.Background(), uc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	if want := "done envs/db, exit=3\n"; res.Stdout != want {
		t.Fatalf("stdout = %q, want %q", res.Stdout, want)
	}
}

func TestRunBadTemplate(t *testing.T) {
	op := New("bad", `echo {{ .NoSuchField }}`, 0)
	res := op.Run(context.Background(), newRC())
	if res.Status != engine.StatusFailed || res.Err == nil {
		t.Fatalf("want render failure, got %v / %v", res.Status, res.Err)
	}
}

func TestRunExitCode(t *testing.T) {
	op := New("fail", `echo oops; exit 3`, 0)
	res := op.Run(context.Background(), newRC())
	if res.Status != engine.StatusFailed {
		t.Fatalf("status = %v, want failed", res.Status)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "oops") {
		t.Fatalf("stdout = %q, want output captured", res.Stdout)
	}
}

func TestRunTimeout(t *testing.T) {
	op := New("slow", `sleep 5`, 100*time.Millisecond)
	start := time.Now()
	res := op.Run(context.Background(), newRC())
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not interrupt the command")
	}
	if res.Status != engine.StatusFailed || res.Err == nil {
		t.Fatalf("want timeout failure, got %v / %v", res.Status, res.Err)
	}
	if !strings.Contains(res.Err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout mention", res.Err)
	}
}

func TestRunStderrCaptured(t *testing.T) {
	op := New("err", `echo to-stderr 1>&2`, 0)
	res := op.Run(context.Background(), newRC())
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	if !strings.Contains(res.Stdout, "to-stderr") {
		t.Fatalf("stdout = %q, want stderr captured", res.Stdout)
	}
}

func TestOutputTruncated(t *testing.T) {
	op := New("big", `head -c 200000 /dev/zero | tr '\0' 'x'`, 0)
	res := op.Run(context.Background(), newRC())
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	if len(res.Stdout) > MaxOutput+100 {
		t.Fatalf("stdout len = %d, want truncated to ~%d", len(res.Stdout), MaxOutput)
	}
	if !strings.HasSuffix(res.Stdout, "[truncated]") {
		t.Fatal("want truncation marker")
	}
}
