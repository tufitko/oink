package discovery

import (
	"context"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/gruntwork-io/terragrunt/pkg/config"
	tglog "github.com/gruntwork-io/terragrunt/pkg/log"
	"github.com/gruntwork-io/terragrunt/pkg/log/format"
	"github.com/gruntwork-io/terragrunt/pkg/options"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Collector resolves per-unit information without a full terragrunt parse:
// the SourceFiles of a unit (configs it reads plus the files of its local
// terraform module, recursively over local submodules) and its evaluated
// top-level locals. Module file sets are cached per directory — many units
// share the same module. Safe for concurrent use.
//
// terraform.source and locals are evaluated from the unit's terragrunt.hcl by
// parsing with hcl/v2 and using terragrunt's own function implementations
// (config.EarlyStackParseFunctions: get_repo_root, get_env,
// find_in_parent_folders, the terraform built-ins, ...). The heavier
// alternatives were measured and rejected: `terragrunt render` resolves
// dependency outputs (~30s per unit, cloud credentials);
// config.PartialParseConfigFile re-parses includes per unit (~60s over the
// whole repo vs ~2s for this approach). The tradeoff: locals inherited via
// include blocks are not merged — only the unit's own locals are evaluated.
type Collector struct {
	repoRoot string // absolute
	logger   tglog.Logger
	opts     *options.TerragruntOptions
	// parseCtx carries terragrunt's context-scoped caches (run_cmd etc.)
	// shared across all units.
	parseCtx context.Context
	// gitRoot is `git rev-parse --show-toplevel` of repoRoot, resolved once:
	// terragrunt's get_repo_root family forks git per call, which dominates
	// discovery on thousands of units. Empty when repoRoot is not in a git
	// repository — then terragrunt's original implementations are kept.
	// Caveat: a nested git repository below repoRoot would have a different
	// root; not supported.
	gitRoot string

	mu          sync.Mutex
	moduleCache map[string][]string
}

// NewCollector builds a Collector rooted at repoRoot.
func NewCollector(repoRoot string) (*Collector, error) {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	// Resolve the repo root through symlinks so module files reached through
	// symlinks (resolved to their real locations) yield clean repo-relative
	// paths.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	opts, err := options.NewTerragruntOptionsWithConfigPath(filepath.Join(abs, config.DefaultTerragruntConfigPath))
	if err != nil {
		return nil, err
	}
	// Terragrunt's CLI fills opts.Env from the process environment itself
	// (internal/util); the library constructor leaves it empty — get_env()
	// would see nothing.
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			opts.Env[k] = v
		}
	}
	logger := tglog.New(
		tglog.WithOutput(io.Discard),
		tglog.WithLevel(tglog.ErrorLevel),
		tglog.WithFormatter(format.NewFormatter(format.NewPrettyFormatPlaceholders())),
	)
	return &Collector{
		repoRoot:    abs,
		logger:      logger,
		opts:        opts,
		parseCtx:    config.WithConfigValues(context.Background()),
		gitRoot:     gitTopLevel(abs),
		moduleCache: map[string][]string{},
	}, nil
}

// gitTopLevel returns the git repository root of dir, or "" outside of git.
func gitTopLevel(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SourceFiles merges the unit's reading list with its local module files.
// unitPath is relative to the repo root; the returned paths are too, sorted
// and deduplicated. Safe for concurrent use.
func (c *Collector) SourceFiles(unitPath string, reading []string) []string {
	set := map[string]bool{}
	for _, r := range reading {
		set[filepath.Clean(r)] = true
	}
	if dir, ok := c.localModuleDir(filepath.Join(c.repoRoot, unitPath)); ok {
		for _, f := range c.moduleFiles(dir, map[string]bool{}) {
			set[f] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(set))
}

// Locals evaluates the unit's own top-level locals (best-effort; values that
// cannot be resolved offline are omitted). unitDir is an absolute path.
// Returns an empty map if the unit has no readable terragrunt.hcl.
func (c *Collector) Locals(unitDir string) (map[string]cty.Value, error) {
	body, ok := parseUnitBody(unitDir)
	if !ok {
		return map[string]cty.Value{}, nil
	}
	ctx, ok := c.evalContext(unitDir)
	if !ok {
		return map[string]cty.Value{}, nil
	}
	return evalLocals(body, ctx), nil
}

// HasTerraformSource reports whether the unit's terragrunt.hcl declares a
// terraform block with a source attribute (local or remote). Units without
// one are "parent"/root configs in terragrunt-atlantis-config terms.
// unitDir is an absolute path.
func (c *Collector) HasTerraformSource(unitDir string) bool {
	body, ok := parseUnitBody(unitDir)
	if !ok {
		return false
	}
	for _, block := range body.Blocks {
		if block.Type != "terraform" {
			continue
		}
		if _, ok := block.Body.Attributes["source"]; ok {
			return true
		}
	}
	return false
}

// parseUnitBody reads and parses a unit's terragrunt.hcl into an hclsyntax
// body, returning ok=false if it is missing or unparseable.
func parseUnitBody(unitDir string) (*hclsyntax.Body, bool) {
	return parseFileBody(filepath.Join(unitDir, config.DefaultTerragruntConfigPath))
}

// parseFileBody reads and parses an arbitrary HCL file into an hclsyntax body,
// returning ok=false if it is missing or unparseable.
func parseFileBody(path string) (*hclsyntax.Body, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	f, diags := hclsyntax.ParseConfig(raw, filepath.Base(path), hcl.InitialPos)
	if f == nil || f.Body == nil {
		return nil, false
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok || (diags.HasErrors() && len(body.Blocks) == 0) {
		return nil, false
	}
	return body, true
}

// localModuleDir extracts terraform.source from the unit's terragrunt.hcl —
// or, when the unit has none of its own, from the config files it includes
// (one level; the common `_commons/*.hcl` envcommon pattern) — and resolves
// it to a local module directory. Source expressions are evaluated in the
// unit's context, as terragrunt does for merged includes. Anything that
// cannot be resolved offline (dependency outputs etc.) degrades to "no local
// module" instead of failing discovery.
func (c *Collector) localModuleDir(unitDir string) (string, bool) {
	body, ok := parseUnitBody(unitDir)
	if !ok {
		return "", false
	}

	ctx, ok := c.evalContext(unitDir)
	if !ok {
		return "", false
	}
	evalLocals(body, ctx)

	// The unit's own terraform.source takes precedence.
	if dir, ok := c.sourceFromBody(unitDir, body, ctx); ok {
		return dir, true
	}

	// Otherwise fall back to included configs (envcommon etc.). Their locals
	// are merged in (the unit's own win on conflict) before evaluating source.
	for _, incPath := range includePaths(unitDir, body, ctx) {
		incBody, ok := parseFileBody(incPath)
		if !ok {
			continue
		}
		mergeLocals(incBody, ctx)
		if dir, ok := c.sourceFromBody(unitDir, incBody, ctx); ok {
			return dir, true
		}
	}
	return "", false
}

// sourceFromBody evaluates a terraform.source attribute in body (if any) and
// resolves it to a local module directory.
func (c *Collector) sourceFromBody(unitDir string, body *hclsyntax.Body, ctx *hcl.EvalContext) (string, bool) {
	for _, block := range body.Blocks {
		if block.Type != "terraform" {
			continue
		}
		attr, ok := block.Body.Attributes["source"]
		if !ok {
			continue
		}
		val, diags := attr.Expr.Value(ctx)
		if diags.HasErrors() || !val.IsKnown() || val.IsNull() || val.Type() != cty.String {
			continue
		}
		if dir, ok := c.resolveLocalDir(unitDir, val.AsString()); ok {
			return dir, true
		}
	}
	return "", false
}

// includePaths evaluates the `path` of each include block to an absolute file
// path. Paths that cannot be resolved are skipped.
func includePaths(unitDir string, body *hclsyntax.Body, ctx *hcl.EvalContext) []string {
	var out []string
	for _, block := range body.Blocks {
		if block.Type != "include" {
			continue
		}
		attr, ok := block.Body.Attributes["path"]
		if !ok {
			continue
		}
		val, diags := attr.Expr.Value(ctx)
		if diags.HasErrors() || !val.IsKnown() || val.IsNull() || val.Type() != cty.String {
			continue
		}
		p := val.AsString()
		if !filepath.IsAbs(p) {
			p = filepath.Join(unitDir, p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

// mergeLocals evaluates body's locals and adds the ones not already present
// to ctx's local.* object (existing values — the child's — win on conflict).
func mergeLocals(body *hclsyntax.Body, ctx *hcl.EvalContext) {
	existing := map[string]cty.Value{}
	if cur, ok := ctx.Variables["local"]; ok && cur.CanIterateElements() {
		existing = cur.AsValueMap()
	}
	added := evalLocals(body, ctx) // sets ctx.local to body's locals
	merged := map[string]cty.Value{}
	for k, v := range added {
		merged[k] = v
	}
	for k, v := range existing { // child wins
		merged[k] = v
	}
	if len(merged) == 0 {
		ctx.Variables["local"] = cty.EmptyObjectVal
	} else {
		ctx.Variables["local"] = cty.ObjectVal(merged)
	}
}

// evalContext builds an HCL evaluation context with terragrunt's own function
// implementations scoped to unitDir.
func (c *Collector) evalContext(unitDir string) (*hcl.EvalContext, bool) {
	_, pctx := config.NewParsingContext(c.parseCtx, c.logger, config.WithStrictControls(c.opts.StrictControls))
	pctx.RootWorkingDir = c.repoRoot
	pctx.MaxFoldersToCheck = c.opts.MaxFoldersToCheck
	pctx.DownloadDir = c.opts.DownloadDir
	pctx.Env = c.opts.Env

	funcs, err := config.EarlyStackParseFunctions(c.parseCtx, c.logger, unitDir, pctx)
	if err != nil {
		return nil, false
	}

	// Memoized replacements for the git-backed path functions: same values
	// (derived from the once-resolved git root), no `git rev-parse` fork per
	// unit. See Collector.gitRoot.
	if c.gitRoot != "" {
		if toRoot, err := filepath.Rel(unitDir, c.gitRoot); err == nil {
			funcs[config.FuncNameGetPathToRepoRoot] = constStringFunc(toRoot)
		}
		if fromRoot, err := filepath.Rel(c.gitRoot, unitDir); err == nil {
			funcs[config.FuncNameGetPathFromRepoRoot] = constStringFunc(fromRoot)
		}
		funcs[config.FuncNameGetRepoRoot] = constStringFunc(c.gitRoot)
	}

	return &hcl.EvalContext{
		Variables: map[string]cty.Value{},
		Functions: funcs,
	}, true
}

func constStringFunc(s string) function.Function {
	return function.New(&function.Spec{
		Type: function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) {
			return cty.StringVal(s), nil
		},
	})
}

// evalLocals best-effort evaluates the locals blocks, exposes the
// successfully evaluated ones as local.* in ctx, and returns them — sources
// like `source = "${local.modules}/s3"` are common. Locals referencing other
// locals are handled by iterating to a fixed point; failing attributes
// (dependency outputs etc.) are silently dropped.
func evalLocals(body *hclsyntax.Body, ctx *hcl.EvalContext) map[string]cty.Value {
	vals := map[string]cty.Value{}
	for {
		progress := false
		for _, block := range body.Blocks {
			if block.Type != "locals" {
				continue
			}
			for name, attr := range block.Body.Attributes {
				if _, done := vals[name]; done {
					continue
				}
				ctx.Variables["local"] = cty.ObjectVal(vals)
				val, diags := attr.Expr.Value(ctx)
				if diags.HasErrors() || !val.IsKnown() {
					continue
				}
				vals[name] = val
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	ctx.Variables["local"] = cty.ObjectVal(vals)
	return vals
}

// resolveLocalDir resolves an evaluated source string to an existing local
// directory. Remote sources (git::, URLs, registry) return ok=false.
func (c *Collector) resolveLocalDir(fromDir, src string) (string, bool) {
	if strings.Contains(src, "::") || strings.Contains(src, "://") {
		return "", false
	}
	// `//` separates the module subdirectory in terraform sources; for local
	// paths it is just a path separator.
	src = strings.ReplaceAll(src, "//", "/")
	if !filepath.IsAbs(src) {
		src = filepath.Join(fromDir, src)
	}
	src = filepath.Clean(src)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return "", false
	}
	return src, true
}

// moduleFiles collects the module's .tf files, the files they reference via
// file()/templatefile() and the files of local submodules, recursively.
// Symlinks (the module directory itself, individual files and submodule
// targets) are resolved to their real locations, and the returned paths are
// relative to the repo root — so a symlinked shared module triggers on its
// real files, not on the link.
func (c *Collector) moduleFiles(dir string, visited map[string]bool) []string {
	dir = resolveSymlink(dir)
	if visited[dir] {
		return nil
	}
	visited[dir] = true
	c.mu.Lock()
	cached, ok := c.moduleCache[dir]
	c.mu.Unlock()
	if ok {
		return cached
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		out = append(out, c.realRel(path))

		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f, _ := hclsyntax.ParseConfig(raw, e.Name(), hcl.InitialPos)
		if f == nil || f.Body == nil {
			continue
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}

		out = append(out, c.fileRefs(dir, body)...)

		for _, sub := range localSubmodules(body) {
			subDir := filepath.Clean(filepath.Join(dir, sub))
			if st, err := os.Stat(subDir); err == nil && st.IsDir() {
				out = append(out, c.moduleFiles(subDir, visited)...)
			}
		}
	}
	c.mu.Lock()
	c.moduleCache[dir] = out
	c.mu.Unlock()
	return out
}

// moduleEvalCtx is the context for evaluating module-file expressions:
// ${path.module} is the module directory itself.
var moduleEvalCtx = &hcl.EvalContext{
	Variables: map[string]cty.Value{
		"path": cty.ObjectVal(map[string]cty.Value{"module": cty.StringVal(".")}),
	},
}

// fileRefs walks all expressions of a .tf file and collects literal arguments
// of file-reading functions: file(...), templatefile(...), filebase64(...),
// fileexists(...) etc. ${path.module} resolves to the module directory;
// arguments referencing anything else dynamic are skipped.
func (c *Collector) fileRefs(dir string, body *hclsyntax.Body) []string {
	var out []string
	hclsyntax.VisitAll(body, func(node hclsyntax.Node) hcl.Diagnostics {
		call, ok := node.(*hclsyntax.FunctionCallExpr)
		if !ok || len(call.Args) == 0 || !isFileFunc(call.Name) {
			return nil
		}
		val, diags := call.Args[0].Value(moduleEvalCtx)
		if diags.HasErrors() || !val.IsKnown() || val.IsNull() || val.Type() != cty.String {
			return nil
		}
		p := filepath.Clean(filepath.Join(dir, val.AsString()))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, c.realRel(p))
		}
		return nil
	})
	return out
}

func isFileFunc(name string) bool {
	return name == "templatefile" || strings.HasPrefix(name, "file")
}

// localSubmodules returns the local (./ or ../) module sources declared in the
// file. Sources are evaluated with ${path.module} so interpolated local paths
// resolve; non-local (registry/git) and unresolvable sources are skipped.
func localSubmodules(body *hclsyntax.Body) []string {
	var out []string
	for _, block := range body.Blocks {
		if block.Type != "module" {
			continue
		}
		attr, ok := block.Body.Attributes["source"]
		if !ok {
			continue
		}
		val, diags := attr.Expr.Value(moduleEvalCtx)
		if diags.HasErrors() || !val.IsKnown() || val.IsNull() || val.Type() != cty.String {
			continue
		}
		src := val.AsString()
		if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
			out = append(out, src)
		}
	}
	return out
}

// resolveSymlink returns the real path of p, or p unchanged if it cannot be
// resolved.
func resolveSymlink(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// realRel is rel() after resolving symlinks, so the emitted path points at the
// real file (the one git tracks), not at a symlink to it.
func (c *Collector) realRel(path string) string {
	return c.rel(resolveSymlink(path))
}

func (c *Collector) rel(path string) string {
	if r, err := filepath.Rel(c.repoRoot, path); err == nil {
		return r
	}
	return path
}
