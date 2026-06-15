package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// writeTree creates files under root; map values are file contents.
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

// tempRoot returns a symlink-resolved temp dir: terragrunt and git resolve
// physical paths, and on macOS t.TempDir lives under the /var → /private/var
// symlink.
func tempRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// gitInit makes root a real git repository: terragrunt's
// get_path_to_repo_root runs `git rev-parse --show-toplevel`.
func gitInit(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func newCollector(t *testing.T, root string) *Collector {
	t.Helper()
	c, err := NewCollector(root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// unitConfig builds a terragrunt.hcl with the given source expression.
func unitConfig(source string) string {
	return "terraform {\n  source = " + source + "\n}\n"
}

func TestLocalModuleDirSources(t *testing.T) {
	cases := []struct {
		name   string
		source string // attribute expression as written in HCL
		ok     bool
	}{
		{"literal relative", `"../modules/storage"`, true},
		{"double slash separator", `"../modules//storage"`, true},
		{"get_path_to_repo_root", `"${get_path_to_repo_root()}/modules/storage"`, true},
		{"get_repo_root", `"${get_repo_root()}/modules/storage"`, true},
		{"get_terragrunt_dir", `"${get_terragrunt_dir()}/../modules/storage"`, true},
		{"dirname over find_in_parent_folders", `"${dirname(find_in_parent_folders("marker.hcl"))}/modules/storage"`, true},
		{"git source", `"git::https://github.com/org/modules.git//storage"`, false},
		{"registry source", `"tfr:///terraform-aws-modules/vpc/aws"`, false},
		{"registry shorthand", `"terraform-aws-modules/vpc/aws"`, false},
		{"dependency reference", `"${dependency.vpc.outputs.dir}/storage"`, false},
		{"missing directory", `"../modules/no-such"`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tempRoot(t)
			gitInit(t, root)
			writeTree(t, root, map[string]string{
				"marker.hcl":              "",
				"modules/storage/main.tf": "",
				"unit/terragrunt.hcl":     unitConfig(tc.source),
			})
			c := newCollector(t, root)

			got, ok := c.localModuleDir(filepath.Join(root, "unit"))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.ok, got)
			}
			if tc.ok {
				if want := filepath.Join(root, "modules/storage"); got != want {
					t.Fatalf("dir = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestLocalModuleDirGetEnv(t *testing.T) {
	root := tempRoot(t)
	writeTree(t, root, map[string]string{
		"modules/storage/main.tf": "",
		"unit/terragrunt.hcl":     unitConfig(`"${get_env("OINK_TEST_MODULES")}/storage"`),
	})
	t.Setenv("OINK_TEST_MODULES", filepath.Join(root, "modules"))
	c := newCollector(t, root) // after Setenv: the env snapshot is taken here

	got, ok := c.localModuleDir(filepath.Join(root, "unit"))
	want := filepath.Join(root, "modules/storage")
	if !ok || got != want {
		t.Fatalf("localModuleDir = (%q, %v), want (%q, true)", got, ok, want)
	}
}

func TestLocalModuleDirWithLocals(t *testing.T) {
	root := tempRoot(t)
	gitInit(t, root)
	writeTree(t, root, map[string]string{
		"unit/terragrunt.hcl": `
locals {
  base    = "${get_path_to_repo_root()}/modules"
  derived = "${local.base}/storage"
}

terraform {
  source = local.derived
}
`,
		"modules/storage/main.tf": "",
	})
	c := newCollector(t, root)

	got, ok := c.localModuleDir(filepath.Join(root, "unit"))
	want := filepath.Join(root, "modules/storage")
	if !ok || got != want {
		t.Fatalf("localModuleDir = (%q, %v), want (%q, true)", got, ok, want)
	}
}

func TestLocalModuleDirFromInclude(t *testing.T) {
	// The terraform source is defined in an included envcommon config, not in
	// the unit's own terragrunt.hcl — the common _commons pattern.
	root := tempRoot(t)
	gitInit(t, root)
	writeTree(t, root, map[string]string{
		"_commons/namespaces.hcl": `
locals { mod = "${get_path_to_repo_root()}/modules/ns" }
terraform { source = local.mod }
`,
		"envs/ns/terragrunt.hcl": `
include "envcommon" {
  path = "${get_path_to_repo_root()}/_commons/namespaces.hcl"
}
`,
		"modules/ns/main.tf": "",
	})
	c := newCollector(t, root)

	got, ok := c.localModuleDir(filepath.Join(root, "envs/ns"))
	want := filepath.Join(root, "modules/ns")
	if !ok || got != want {
		t.Fatalf("localModuleDir = (%q, %v), want (%q, true)", got, ok, want)
	}
}

func TestModuleFilesRecursive(t *testing.T) {
	root := tempRoot(t)
	writeTree(t, root, map[string]string{
		"modules/a/main.tf": `
resource "x" "y" {
  policy   = file("${path.module}/files/policy.json")
  template = templatefile("templates/conf.tpl", {})
  dynamic  = file("${var.dynamic}/skip.json")
}

module "sub" {
  source = "./sub"
}

module "remote" {
  source = "terraform-aws-modules/vpc/aws"
}
`,
		"modules/a/files/policy.json":  "{}",
		"modules/a/templates/conf.tpl": "",
		"modules/a/sub/main.tf": `
module "back" {
  source = "../" # cycle back to parent
}
`,
		"modules/a/README.md": "not a tf file",
	})
	c := newCollector(t, root)

	got := c.moduleFiles(filepath.Join(root, "modules/a"), map[string]bool{})
	slices.Sort(got)
	want := []string{
		"modules/a/files/policy.json",
		"modules/a/main.tf",
		"modules/a/sub/main.tf",
		"modules/a/templates/conf.tpl",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("moduleFiles = %v, want %v", got, want)
	}
}

func symlink(t *testing.T, oldRel, newPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldRel, newPath); err != nil {
		t.Fatal(err)
	}
}

func TestModuleFilesResolvesSymlinks(t *testing.T) {
	root := tempRoot(t)
	writeTree(t, root, map[string]string{
		// The module references a local submodule and a same-dir .tf file
		// that are both symlinks into a shared tree.
		"modules/m/main.tf": `
module "sub" { source = "./sub" }
`,
		"shared/sub/main.tf":   `resource "null_resource" "s" {}`,
		"shared/extra/util.tf": `resource "null_resource" "u" {}`,
	})
	// modules/m/sub -> ../../shared/sub (a symlinked submodule directory)
	symlink(t, "../../shared/sub", filepath.Join(root, "modules/m/sub"))
	// modules/m/linked.tf -> ../../shared/extra/util.tf (a symlinked file)
	symlink(t, "../../shared/extra/util.tf", filepath.Join(root, "modules/m/linked.tf"))

	c := newCollector(t, root)
	got := c.moduleFiles(filepath.Join(root, "modules/m"), map[string]bool{})
	slices.Sort(got)
	want := []string{
		"modules/m/main.tf",
		"shared/extra/util.tf", // symlinked file → real path
		"shared/sub/main.tf",   // symlinked submodule → real files
	}
	if !slices.Equal(got, want) {
		t.Fatalf("moduleFiles = %v, want %v", got, want)
	}
}

func TestUnitSourceFilesMergesReadingAndModule(t *testing.T) {
	root := tempRoot(t)
	writeTree(t, root, map[string]string{
		"unit/terragrunt.hcl": unitConfig(`"../modules/m"`),
		"modules/m/main.tf":   "",
	})
	c := newCollector(t, root)

	got := c.SourceFiles("unit", []string{"root.hcl", "./root.hcl"})
	want := []string{"modules/m/main.tf", "root.hcl"}
	if !slices.Equal(got, want) {
		t.Fatalf("SourceFiles = %v, want %v", got, want)
	}

	if files := c.SourceFiles("unit-without-config", nil); files != nil {
		t.Fatalf("want nil for unit without terragrunt.hcl and reading, got %v", files)
	}
}
