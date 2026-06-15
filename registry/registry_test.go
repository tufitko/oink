package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/engine"
)

type nopOp struct{ name string }

func (n *nopOp) Name() string { return n.name }
func (n *nopOp) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	return engine.Result{Status: engine.StatusOK}
}

func nopFactory(name string) Factory {
	return Factory{
		Meta: config.OpMeta{Run: true},
		New: func(bc BuildContext, op config.Operation) (any, error) {
			return &nopOp{name: name}, nil
		},
	}
}

func TestRegisterCustomOperation(t *testing.T) {
	r := New()
	if err := r.Register("custom", nopFactory("custom")); err != nil {
		t.Fatal(err)
	}
	meta, ok := r.Meta("custom")
	if !ok || !meta.Run {
		t.Fatalf("Meta(custom) = %+v, %v", meta, ok)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := New()
	if err := r.Register("dup", nopFactory("dup")); err != nil {
		t.Fatal(err)
	}
	err := r.Register("dup", nopFactory("dup"))
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestBuildUnknownOperation(t *testing.T) {
	r := New()
	cfg := &config.Pipeline{Pipeline: []config.Operation{{Use: "ghost"}}}
	_, err := Build(r, cfg, BuildContext{})
	if err == nil || !strings.Contains(err.Error(), "pipeline[0]") {
		t.Fatalf("want unknown op error with path, got %v", err)
	}
}

func TestBuildOnFailureMapping(t *testing.T) {
	r := New()
	if err := r.Register("nop", nopFactory("nop")); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Pipeline{Pipeline: []config.Operation{
		{Use: "nop"},
		{Use: "nop", OnFailure: config.OnFailureContinue},
	}}
	plan, err := Build(r, cfg, BuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pipeline[0].OnFailure != engine.OnFailureTerminate {
		t.Fatalf("default on_failure = %v, want terminate", plan.Pipeline[0].OnFailure)
	}
	if plan.Pipeline[1].OnFailure != engine.OnFailureContinue {
		t.Fatalf("on_failure = %v, want continue", plan.Pipeline[1].OnFailure)
	}
}
