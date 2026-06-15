package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tufitko/oink/engine"
	"github.com/tufitko/oink/ops/units"
)

func sampleResults() map[string]*units.UnitResult {
	okRes := engine.Result{Status: engine.StatusOK}
	return map[string]*units.UnitResult{
		"net": {
			Unit:   &engine.Unit{Path: "net"},
			Status: engine.StatusOK,
			Ops: []units.OpResult{
				{Name: "unit-hello", Result: okRes},
				{Name: "terragrunt", Result: okRes},
			},
		},
		"db": {
			Unit:   &engine.Unit{Path: "db"},
			Status: engine.StatusFailed,
			Ops: []units.OpResult{
				{Name: "unit-hello", Result: engine.Result{Status: engine.StatusFailed, ExitCode: 1, Err: errors.New("boom")}},
			},
		},
		"app": {
			Unit:   &engine.Unit{Path: "app", Deps: []string{"db"}},
			Status: engine.StatusSkipped,
		},
	}
}

func newRC(t *testing.T, results map[string]*units.UnitResult) *engine.RunContext {
	rc := &engine.RunContext{
		Run:    engine.RunInfo{RunID: "r1", Command: engine.CommandPlan, RepoRoot: t.TempDir()},
		Events: engine.NopSink{},
		Vars:   engine.NewVars(),
	}
	if results != nil {
		engine.Set(rc.Vars, units.KeyResults, results)
	}
	return rc
}

func TestTableOutput(t *testing.T) {
	var buf bytes.Buffer
	rc := newRC(t, sampleResults())
	res := New("", &buf).Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}

	out := buf.String()
	for _, want := range []string{"UNIT", "STATUS", "unit-hello", "terragrunt", "net", "db", "app", "skipped", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "TOTAL: 3 units — 1 ok, 0 warn, 1 failed, 1 skipped") {
		t.Errorf("unexpected totals:\n%s", out)
	}
}

func TestRunJSONWritten(t *testing.T) {
	var buf bytes.Buffer
	rc := newRC(t, sampleResults())
	res := New("", &buf).Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}

	raw, err := os.ReadFile(filepath.Join(rc.Run.RepoRoot, DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		RunID   string `json:"run_id"`
		Command string `json:"command"`
		Units   []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
			Ops    []struct {
				Name     string `json:"name"`
				Status   string `json:"status"`
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error"`
			} `json:"ops"`
		} `json:"units"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.RunID != "r1" || doc.Command != "plan" {
		t.Fatalf("run info = %+v", doc)
	}
	if len(doc.Units) != 3 {
		t.Fatalf("got %d units, want 3", len(doc.Units))
	}
	// Sorted by path: app, db, net.
	if doc.Units[0].Path != "app" || doc.Units[1].Path != "db" || doc.Units[2].Path != "net" {
		t.Fatalf("unexpected unit order: %+v", doc.Units)
	}
	dbOps := doc.Units[1].Ops
	if len(dbOps) != 1 || dbOps[0].Status != "failed" || dbOps[0].ExitCode != 1 || dbOps[0].Error != "boom" {
		t.Fatalf("db ops = %+v", dbOps)
	}
}

func TestNoUnitResults(t *testing.T) {
	var buf bytes.Buffer
	rc := newRC(t, nil)
	res := New("", &buf).Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	if !strings.Contains(buf.String(), "no unit results") {
		t.Fatalf("output = %q", buf.String())
	}
	if _, err := os.Stat(filepath.Join(rc.Run.RepoRoot, DefaultPath)); err != nil {
		t.Fatalf("run.json must be written even without unit results: %v", err)
	}
}
