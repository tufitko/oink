package terragrunt

import (
	"context"
	"strings"
	"testing"

	"github.com/tufitko/oink/engine"
)

func newUC(command engine.Command, args ...string) *engine.UnitContext {
	return &engine.UnitContext{
		Run: &engine.RunContext{
			Run:    engine.RunInfo{RunID: "r1", Command: command, Args: args},
			Events: engine.NopSink{},
			Vars:   engine.NewVars(),
		},
		Unit: &engine.Unit{Path: "envs/db"},
		Vars: engine.NewVars(),
	}
}

func TestDryRunEchoesCommand(t *testing.T) {
	op := New("plan", true, 0)
	res := op.RunUnit(context.Background(), newUC(engine.CommandPlan))
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	for _, part := range []string{"dry-run", "plan", "envs/db", "--working-dir"} {
		if !strings.Contains(res.Stdout, part) {
			t.Errorf("stdout = %q, want to contain %q", res.Stdout, part)
		}
	}
}

func TestCommandDefaultsToRunCommand(t *testing.T) {
	op := New("", true, 0)
	res := op.RunUnit(context.Background(), newUC(engine.CommandApply))
	if !strings.Contains(res.Stdout, "apply") {
		t.Fatalf("stdout = %q, want apply from RunInfo.Command", res.Stdout)
	}
}

func TestPassthroughArgs(t *testing.T) {
	op := New("", true, 0)
	res := op.RunUnit(context.Background(), newUC(engine.CommandPlan, "-refresh=false", "-lock=false"))
	// Passthrough args sit between the command and oink's own flags.
	for _, part := range []string{"plan", "-refresh=false", "-lock=false", "--non-interactive", "--working-dir"} {
		if !strings.Contains(res.Stdout, part) {
			t.Errorf("stdout = %q, want to contain %q", res.Stdout, part)
		}
	}
	if idx, idxArg := strings.Index(res.Stdout, "-refresh=false"), strings.Index(res.Stdout, "--non-interactive"); idx > idxArg {
		t.Errorf("passthrough args must come before oink flags: %q", res.Stdout)
	}
}
