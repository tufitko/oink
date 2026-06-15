package discovery

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tufitko/oink/engine"
)

func requireTerragrunt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("terragrunt"); err != nil {
		t.Skip("terragrunt not in PATH")
	}
}

func newRC(t *testing.T, repoRoot string) *engine.RunContext {
	t.Helper()
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	return &engine.RunContext{
		Run:    engine.RunInfo{RunID: "r1", RepoRoot: abs},
		Events: engine.NopSink{},
		Vars:   engine.NewVars(),
	}
}

func graphPaths(g *engine.UnitGraph) []string {
	var out []string
	for _, u := range g.All() {
		out = append(out, u.Path)
	}
	slices.Sort(out)
	return out
}

// TestTerragruntFind runs real terragrunt against the fixture mini-repo.
// Skipped when terragrunt is not in PATH.
func TestTerragruntFind(t *testing.T) {
	requireTerragrunt(t)
	rc := newRC(t, "../../testdata/repo")

	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	if rc.Units == nil {
		t.Fatal("units not set in context")
	}
	want := []string{"app", "db", "net", "standalone", "worker"}
	if got := graphPaths(rc.Units); !slices.Equal(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
	db, _ := rc.Units.ByPath("db")
	if !slices.Contains(db.Deps, "net") {
		t.Fatalf("db deps = %v, want to contain net", db.Deps)
	}
	if !strings.Contains(res.Stdout, "5 units discovered") {
		t.Fatalf("stdout = %q", res.Stdout)
	}

	// SourceFiles: includes from `find --reading`.
	net, _ := rc.Units.ByPath("net")
	if !slices.Equal(net.SourceFiles, []string{"root.hcl"}) {
		t.Fatalf("net source files = %v, want [root.hcl]", net.SourceFiles)
	}
	// SourceFiles: local module via ${get_path_to_repo_root()} (db) and a
	// literal relative source (app) — module .tf files, file() references and
	// local submodule files, recursively.
	wantModule := []string{
		"modules/storage/files/policy.json",
		"modules/storage/main.tf",
		"modules/storage/sub/main.tf",
		"modules/storage/variables.tf",
	}
	if !slices.Equal(db.SourceFiles, wantModule) {
		t.Fatalf("db source files = %v, want %v", db.SourceFiles, wantModule)
	}
	app, _ := rc.Units.ByPath("app")
	if !slices.Equal(app.SourceFiles, wantModule) {
		t.Fatalf("app source files = %v, want %v", app.SourceFiles, wantModule)
	}
	worker, _ := rc.Units.ByPath("worker")
	if worker.SourceFiles != nil {
		t.Fatalf("worker source files = %v, want nil (no local source, no includes)", worker.SourceFiles)
	}
}

func TestTerragruntFindBadRepoRoot(t *testing.T) {
	requireTerragrunt(t)
	rc := newRC(t, "../../testdata")
	rc.Run.RepoRoot = filepath.Join(rc.Run.RepoRoot, "no-such-dir")

	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusFailed || res.Err == nil {
		t.Fatalf("want failure for missing repo root, got %v / %v", res.Status, res.Err)
	}
}
