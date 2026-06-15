package oink

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tufitko/oink/config"
	"github.com/tufitko/oink/engine"
	"github.com/tufitko/oink/registry"
)

// tempRepo copies the fixture terragrunt mini-repo into a temporary repo root
// so that run.json does not pollute testdata. Discovery runs real terragrunt,
// so tests using it skip when terragrunt is not in PATH.
func tempRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("terragrunt"); err != nil {
		t.Skip("terragrunt not in PATH")
	}
	// EvalSymlinks: on macOS t.TempDir lives under the /var → /private/var
	// symlink; terragrunt resolves dependency paths through it, and they stop
	// matching the unit paths relative to the repo root.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS("testdata/repo")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readRunJSON(t *testing.T, repoRoot string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Units []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"units"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, u := range doc.Units {
		statuses[u.Path] = u.Status
	}
	return statuses
}

func TestE2EExamplePipeline(t *testing.T) {
	repoRoot := tempRepo(t)
	var out bytes.Buffer

	status, err := Run(context.Background(), Options{
		Run: engine.RunInfo{
			RunID:       "e2e-1",
			Command:     engine.CommandPlan,
			ChangedDirs: []string{"db"},
			RepoRoot:    repoRoot,
		},
		ConfigPath: ".tgops.example.yaml",
		Stdout:     &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != engine.StatusOK {
		t.Fatalf("status = %v, want ok\noutput:\n%s", status, out.String())
	}

	text := out.String()
	// The hello operation sees the graph after discovery: db + app + worker.
	if !strings.Contains(text, "run e2e-1: 3 units affected") {
		t.Errorf("hello output missing:\n%s", text)
	}
	// The per-unit echo ran for every unit.
	for _, unit := range []string{"db", "app", "worker"} {
		if !strings.Contains(text, "processing "+unit) {
			t.Errorf("missing per-unit output for %s", unit)
		}
	}
	// NDJSON events are emitted independently of report.
	var startedEvents int
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("invalid NDJSON line: %q", line)
			continue
		}
		if e["event"] == "started" {
			startedEvents++
		}
	}
	// 4 run operations + finally report + 3 units × 3 operations = 14 started.
	if startedEvents != 14 {
		t.Errorf("started events = %d, want 14", startedEvents)
	}
	// The report table.
	if !strings.Contains(text, "TOTAL: 3 units — 3 ok, 0 warn, 0 failed, 0 skipped") {
		t.Errorf("report totals missing:\n%s", text)
	}

	want := map[string]string{"db": "ok", "app": "ok", "worker": "ok"}
	got := readRunJSON(t, repoRoot)
	if len(got) != len(want) {
		t.Fatalf("run.json units = %v, want %v", got, want)
	}
	for path, status := range want {
		if got[path] != status {
			t.Errorf("run.json %s = %s, want %s", path, got[path], status)
		}
	}
}

func TestE2EFailingUnitSkipsDependents(t *testing.T) {
	repoRoot := tempRepo(t)
	var out bytes.Buffer

	status, err := Run(context.Background(), Options{
		Run: engine.RunInfo{
			RunID:    "e2e-2",
			Command:  engine.CommandPlan,
			RepoRoot: repoRoot,
		},
		ConfigPath: "testdata/failing.tgops.yaml",
		Stdout:     &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	// units failed (db FAILED), but on_failure: continue → WARN at run level.
	if status != engine.StatusWarn {
		t.Fatalf("status = %v, want warn\noutput:\n%s", status, out.String())
	}

	want := map[string]string{
		"net":        "ok",
		"db":         "failed",
		"app":        "skipped",
		"worker":     "skipped",
		"standalone": "ok",
	}
	got := readRunJSON(t, repoRoot)
	if len(got) != len(want) {
		t.Fatalf("run.json units = %v, want %v", got, want)
	}
	for path, status := range want {
		if got[path] != status {
			t.Errorf("%s = %s, want %s", path, got[path], status)
		}
	}
	if !strings.Contains(out.String(), "TOTAL: 5 units — 2 ok, 0 warn, 1 failed, 2 skipped") {
		t.Errorf("report totals missing:\n%s", out.String())
	}
}

// customNotify is an example of an embedding application's custom run operation.
type customNotify struct{ notified *bool }

func (c *customNotify) Name() string { return "notify" }
func (c *customNotify) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	*c.notified = true
	return engine.Result{Status: engine.StatusOK}
}

func TestEmbeddingCustomOperation(t *testing.T) {
	notified := false
	reg := DefaultRegistry()
	reg.MustRegister("notify", registry.Factory{
		Meta: config.OpMeta{Run: true},
		New: func(bc registry.BuildContext, op config.Operation) (any, error) {
			return &customNotify{notified: &notified}, nil
		},
	})

	cfg, err := config.Parse([]byte(`
pipeline:
  - use: notify
`))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	status, err := Run(context.Background(), Options{
		Run:      engine.RunInfo{RunID: "embed-1", Command: engine.CommandPlan},
		Config:   cfg,
		Registry: reg,
		Stdout:   &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != engine.StatusOK {
		t.Fatalf("status = %v\noutput:\n%s", status, out.String())
	}
	if !notified {
		t.Fatal("custom operation was not executed")
	}
}

func TestRunInvalidConfigError(t *testing.T) {
	cfg, err := config.Parse([]byte(`
pipeline:
  - use: units
    with:
      pipeline:
        - use: exec
          with: { run: 'echo hi' }
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), Options{
		Run:    engine.RunInfo{RunID: "bad-1"},
		Config: cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("want requires/provides config error, got %v", err)
	}
}
