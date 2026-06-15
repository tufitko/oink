package atlantis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		abs := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fixtureRepo builds a git+terragrunt repo and returns its (symlink-resolved)
// root. Skips when terragrunt is not in PATH.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("terragrunt"); err != nil {
		t.Skip("terragrunt not in PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{
		"modules/m/main.tf": `resource "null_resource" "x" {}`,
		// a: leaf unit with a local module + a workflow override local.
		"a/terragrunt.hcl": `
terraform { source = "../modules/m" }
locals { atlantis_workflow = "wf-a" }
`,
		// b: depends on a, same module.
		"b/terragrunt.hcl": `
terraform { source = "../modules/m" }
dependency "a" { config_path = "../a" }
`,
		// c: depends on b but explicitly skipped.
		"c/terragrunt.hcl": `
dependency "b" { config_path = "../b" }
locals { atlantis_skip = true }
`,
		// d: no terraform source → a "parent" config.
		"d/terragrunt.hcl": `locals { note = "parent" }`,
	})
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return root
}

func projectByDir(cfg *Config, dir string) *Project {
	for i := range cfg.Projects {
		if cfg.Projects[i].Dir == dir {
			return &cfg.Projects[i]
		}
	}
	return nil
}

func dirs(cfg *Config) []string {
	out := make([]string, len(cfg.Projects))
	for i, p := range cfg.Projects {
		out[i] = p.Dir
	}
	return out
}

func baseOpts(root string) Options {
	return Options{
		RepoRoot:               root,
		Autoplan:               true,
		Parallel:               true,
		IgnoreParentTerragrunt: true,
		CascadeDependencies:    true,
		ExecutionOrderGroups:   true,
		DefaultWorkflow:        "terragrunt",
		ApplyRequirements:      []string{"approved"},
	}
}

func TestGenerateProjectsAndOrder(t *testing.T) {
	root := fixtureRepo(t)
	cfg, err := Generate(context.Background(), baseOpts(root))
	if err != nil {
		t.Fatal(err)
	}

	// c is skipped (atlantis_skip), d is a parent (ignore-parent-terragrunt).
	if got := dirs(cfg); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("project dirs = %v, want [a b]", got)
	}
	if cfg.Version != 3 || !cfg.ParallelPlan || !cfg.ParallelApply {
		t.Fatalf("top-level fields off: %+v", cfg)
	}

	a := projectByDir(cfg, "a")
	b := projectByDir(cfg, "b")

	// Workflow: a overridden by local, b from the default flag.
	if a.Workflow != "wf-a" {
		t.Errorf("a.workflow = %q, want wf-a (local override)", a.Workflow)
	}
	if b.Workflow != "terragrunt" {
		t.Errorf("b.workflow = %q, want terragrunt (default)", b.Workflow)
	}

	// Autoplan + apply requirements.
	if !a.Autoplan.Enabled {
		t.Error("a.autoplan.enabled = false, want true")
	}
	if a.ApplyRequirements == nil || !slices.Equal(*a.ApplyRequirements, []string{"approved"}) {
		t.Errorf("a.apply_requirements = %v, want [approved]", a.ApplyRequirements)
	}

	// Execution order: a (leaf) before b (depends on a).
	if a.ExecutionOrderGroup == nil || *a.ExecutionOrderGroup != 0 {
		t.Errorf("a group = %v, want 0", a.ExecutionOrderGroup)
	}
	if b.ExecutionOrderGroup == nil || *b.ExecutionOrderGroup != 1 {
		t.Errorf("b group = %v, want 1", b.ExecutionOrderGroup)
	}

	// when_modified: own globs, the module file, and the dependency config.
	if !slices.Equal(a.Autoplan.WhenModified[:2], []string{"*.hcl", "*.tf*"}) {
		t.Errorf("a when_modified prefix = %v", a.Autoplan.WhenModified)
	}
	if !slices.Contains(a.Autoplan.WhenModified, "../modules/m/*.tf*") {
		t.Errorf("a when_modified missing module file: %v", a.Autoplan.WhenModified)
	}
	if !slices.Contains(b.Autoplan.WhenModified, "../a/terragrunt.hcl") {
		t.Errorf("b when_modified missing dependency config: %v", b.Autoplan.WhenModified)
	}
	if !slices.Contains(b.Autoplan.WhenModified, "../modules/m/*.tf*") {
		t.Errorf("b when_modified missing module file: %v", b.Autoplan.WhenModified)
	}
}

func TestGenerateKeepParents(t *testing.T) {
	root := fixtureRepo(t)
	opts := baseOpts(root)
	opts.IgnoreParentTerragrunt = false

	cfg, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	// d (no source) is now included; c still skipped via local.
	// (Order is by execution group then dir: d is group 0, so it precedes b.)
	got := dirs(cfg)
	slices.Sort(got)
	if !slices.Equal(got, []string{"a", "b", "d"}) {
		t.Fatalf("project dirs = %v, want {a b d}", got)
	}
}

func TestGenerateParentRootHcl(t *testing.T) {
	root := fixtureRepo(t)
	// Add a parent config at the repo root (no terragrunt.hcl there).
	writeTree(t, root, map[string]string{"root.hcl": `locals { x = 1 }`})

	// Parents ignored (default): no "." project.
	cfg, err := Generate(context.Background(), baseOpts(root))
	if err != nil {
		t.Fatal(err)
	}
	if projectByDir(cfg, ".") != nil {
		t.Fatal("with ignore-parent-terragrunt=true, root.hcl must not be a project")
	}

	// Parents kept: a content-less "." project appears.
	opts := baseOpts(root)
	opts.IgnoreParentTerragrunt = false
	cfg, err = Generate(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	root2 := projectByDir(cfg, ".")
	if root2 == nil {
		t.Fatal("with ignore-parent-terragrunt=false, root.hcl dir must be a project")
	}
	if !slices.Equal(root2.Autoplan.WhenModified, []string{"*.hcl", "*.tf*"}) {
		t.Fatalf(`. when_modified = %v, want ["*.hcl" "*.tf*"]`, root2.Autoplan.WhenModified)
	}
}

func TestGenerateCreateProjectName(t *testing.T) {
	root := fixtureRepo(t)
	opts := baseOpts(root)
	opts.CreateProjectName = true
	opts.CreateWorkspace = true

	cfg, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	a := projectByDir(cfg, "a")
	if a.Name != "a" || a.Workspace != "a" {
		t.Fatalf("a name/workspace = %q/%q, want a/a", a.Name, a.Workspace)
	}
}

func TestGenerateDropsModuleDataFiles(t *testing.T) {
	if _, err := exec.LookPath("terragrunt"); err != nil {
		t.Skip("terragrunt not in PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{
		"modules/m/main.tf": `
resource "null_resource" "x" {
  triggers = {
    conf = templatefile("${path.module}/conf.tpl", {})
    pol  = file("${path.module}/policy.json")
  }
}
`,
		"modules/m/conf.tpl":    "x",
		"modules/m/policy.json": "{}",
		"a/terragrunt.hcl":      `terraform { source = "../modules/m" }`,
	})
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	opts := baseOpts(root)
	cfg, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	wm := projectByDir(cfg, "a").Autoplan.WhenModified
	if !slices.Contains(wm, "../modules/m/*.tf*") {
		t.Errorf("want module tf glob, got %v", wm)
	}
	for _, bad := range []string{"../modules/m/conf.tpl", "../modules/m/policy.json"} {
		if slices.Contains(wm, bad) {
			t.Errorf("data file %q must not be listed (like terragrunt-atlantis-config): %v", bad, wm)
		}
	}
}

func TestGenerateNoCascade(t *testing.T) {
	root := fixtureRepo(t)
	opts := baseOpts(root)
	opts.IgnoreDependencyBlocks = true

	cfg, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	b := projectByDir(cfg, "b")
	// With dependency blocks ignored, b does not reference a's config.
	if slices.Contains(b.Autoplan.WhenModified, "../a/terragrunt.hcl") {
		t.Errorf("b when_modified should not include dep config: %v", b.Autoplan.WhenModified)
	}
	if b.ExecutionOrderGroup == nil || *b.ExecutionOrderGroup != 1 {
		// execution order still uses the graph (Deps), independent of when_modified.
		t.Errorf("b group = %v, want 1", b.ExecutionOrderGroup)
	}
}

func TestMarshalShape(t *testing.T) {
	cfg := &Config{
		Version:       3,
		ParallelPlan:  true,
		ParallelApply: true,
		Projects: []Project{{
			Dir:      "envs/db",
			Autoplan: Autoplan{Enabled: true, WhenModified: []string{"*.hcl", "*.tf*"}},
		}},
	}
	out, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"version: 3", "parallel_plan: true", "dir: envs/db", "when_modified:", "enabled: true"} {
		if !strings.Contains(got, want) {
			t.Errorf("marshaled config missing %q:\n%s", want, got)
		}
	}
}
