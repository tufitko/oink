package atlantis

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/tufitko/oink/engine"
	"github.com/tufitko/oink/ops/discovery"
)

// Options configures generation. Field names and defaults mirror the
// `terragrunt-atlantis-config generate` flags.
type Options struct {
	RepoRoot string

	Autoplan               bool
	AutoMerge              bool
	Parallel               bool
	IgnoreParentTerragrunt bool
	IgnoreDependencyBlocks bool
	CascadeDependencies    bool
	CreateWorkspace        bool
	CreateProjectName      bool
	UseProjectMarkers      bool
	ExecutionOrderGroups   bool
	DependsOn              bool

	DefaultWorkflow   string
	ApplyRequirements []string
	TerraformVersion  string
	SilencePRComments []string
	RepoLocks         string
	Filter            []string

	// PreserveWorkflows copies the `workflows:` block from an existing config
	// at this path into the output. Empty disables preservation.
	PreserveWorkflows string

	// NumExecutors bounds generation concurrency (0 → GOMAXPROCS).
	NumExecutors int
}

// parentConfigName is the terragrunt root/parent config that
// terragrunt-atlantis-config also turns into a project (when parents are not
// ignored). Its hardcoded name there is "root.hcl".
const parentConfigName = "root.hcl"

// discoverParentUnits finds directories that hold a parent config
// (root.hcl) but are not terragrunt units themselves, returning them as
// synthetic units (no deps, no source). Hidden and .terragrunt-cache
// directories are skipped.
func discoverParentUnits(repoRoot string, existing map[string]bool) ([]*engine.Unit, error) {
	var out []*engine.Unit
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != repoRoot && (strings.HasPrefix(d.Name(), ".") || d.Name() == ".terragrunt-cache") {
			return fs.SkipDir
		}
		if !fileExists(filepath.Join(path, parentConfigName)) {
			return nil
		}
		// Directories that are real units are emitted by discovery already.
		if fileExists(filepath.Join(path, "terragrunt.hcl")) || fileExists(filepath.Join(path, "terragrunt.hcl.json")) {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.Clean(rel)
		if existing[rel] {
			return nil
		}
		out = append(out, &engine.Unit{Path: rel})
		return nil
	})
	return out, err
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// resolvedUnit pairs a discovered unit with its atlantis_* locals.
type resolvedUnit struct {
	unit   *engine.Unit
	locals unitLocals
	source bool // has a terraform { source } block
}

// Generate builds the Atlantis config for the repository at opts.RepoRoot.
func Generate(ctx context.Context, opts Options) (*Config, error) {
	repoRoot, err := filepath.Abs(opts.RepoRoot)
	if err != nil {
		return nil, err
	}

	units, err := discovery.Discover(ctx, repoRoot)
	if err != nil {
		return nil, err
	}

	// With parents kept, also emit a project per parent config directory
	// (root.hcl with no terragrunt unit), mirroring terragrunt-atlantis-config.
	if !opts.IgnoreParentTerragrunt {
		existing := make(map[string]bool, len(units))
		for _, u := range units {
			existing[u.Path] = true
		}
		parents, err := discoverParentUnits(repoRoot, existing)
		if err != nil {
			return nil, err
		}
		units = append(units, parents...)
	}

	resolved, err := resolveUnits(repoRoot, units, opts.NumExecutors)
	if err != nil {
		return nil, err
	}

	byPath := make(map[string]*engine.Unit, len(units))
	for _, u := range units {
		byPath[u.Path] = u
	}

	cfg := &Config{
		Version:       3,
		AutoMerge:     opts.AutoMerge,
		ParallelPlan:  opts.Parallel,
		ParallelApply: opts.Parallel,
	}

	emitted := map[string]bool{} // dirs that became projects
	for _, ru := range resolved {
		if !includeUnit(ru, opts) {
			continue
		}
		emitted[ru.unit.Path] = true
	}

	for _, ru := range resolved {
		if !emitted[ru.unit.Path] {
			continue
		}
		cfg.Projects = append(cfg.Projects, buildProject(ru, byPath, repoRoot, opts))
	}

	sort.Slice(cfg.Projects, func(i, j int) bool { return cfg.Projects[i].Dir < cfg.Projects[j].Dir })

	if opts.ExecutionOrderGroups || opts.DependsOn {
		assignExecutionOrder(cfg.Projects, byPath, emitted, opts)
	}

	if opts.PreserveWorkflows != "" {
		if wf, err := readWorkflows(opts.PreserveWorkflows); err != nil {
			return nil, err
		} else if wf != nil {
			cfg.Workflows = wf
		}
	}

	return cfg, nil
}

// resolveUnits reads each unit's atlantis_* locals and source-block presence
// in parallel (the partial parse is the slow part, ~ms per unit).
func resolveUnits(repoRoot string, units []*engine.Unit, numExecutors int) ([]resolvedUnit, error) {
	coll, err := discovery.NewCollector(repoRoot)
	if err != nil {
		return nil, err
	}

	out := make([]resolvedUnit, len(units))
	var wg sync.WaitGroup
	limit := numExecutors
	if limit <= 0 {
		limit = runtime.GOMAXPROCS(0)
	}
	sem := make(chan struct{}, limit)
	var mu sync.Mutex
	var firstErr error

	for i, u := range units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			unitDir := filepath.Join(repoRoot, u.Path)
			vals, err := coll.Locals(unitDir)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			out[i] = resolvedUnit{
				unit:   u,
				locals: resolveLocals(vals),
				source: coll.HasTerraformSource(unitDir),
			}
		}()
	}
	wg.Wait()
	return out, firstErr
}

// includeUnit decides whether a unit becomes an Atlantis project.
func includeUnit(ru resolvedUnit, opts Options) bool {
	if ru.unit == nil {
		return false
	}
	if ru.locals.Skip {
		return false
	}
	if opts.UseProjectMarkers && !ru.locals.MarkedProject {
		return false
	}
	if opts.IgnoreParentTerragrunt && !ru.source {
		return false
	}
	if len(opts.Filter) > 0 && !matchesFilter(ru.unit.Path, opts.Filter) {
		return false
	}
	return true
}

func buildProject(ru resolvedUnit, byPath map[string]*engine.Unit, repoRoot string, opts Options) Project {
	u := ru.unit

	p := Project{
		Dir: filepath.ToSlash(u.Path),
		Autoplan: Autoplan{
			Enabled:      resolveAutoplan(ru, opts),
			WhenModified: whenModified(u, byPath, repoRoot, ru.locals.ExtraDeps, opts),
		},
	}

	p.Workflow = opts.DefaultWorkflow
	if ru.locals.Workflow != "" {
		p.Workflow = ru.locals.Workflow
	}

	p.TerraformVersion = opts.TerraformVersion
	if ru.locals.TerraformVersion != "" {
		p.TerraformVersion = ru.locals.TerraformVersion
	}

	p.ApplyRequirements = resolveApplyRequirements(ru, opts)

	if ru.locals.SilencePRComments != nil {
		p.SilencePRComments = *ru.locals.SilencePRComments
	} else if len(opts.SilencePRComments) > 0 {
		p.SilencePRComments = opts.SilencePRComments
	}

	if opts.CreateProjectName {
		p.Name = sanitizeName(p.Dir)
	}
	if opts.CreateWorkspace {
		p.Workspace = sanitizeName(p.Dir)
	}
	if opts.RepoLocks != "" {
		p.RepoLocks = &RepoLocks{Mode: opts.RepoLocks}
	}

	return p
}

func resolveAutoplan(ru resolvedUnit, opts Options) bool {
	if ru.locals.AutoPlan != nil {
		return *ru.locals.AutoPlan
	}
	return opts.Autoplan
}

func resolveApplyRequirements(ru resolvedUnit, opts Options) *[]string {
	if ru.locals.ApplyRequirements != nil {
		return ru.locals.ApplyRequirements
	}
	if len(opts.ApplyRequirements) == 0 {
		return nil
	}
	reqs := append([]string(nil), opts.ApplyRequirements...)
	return &reqs
}

// whenModified builds the autoplan trigger list for a unit: its own files,
// then (transitively, if cascade) its dependencies' configs and source files.
// Paths are relative to the unit directory, Unix-style, deduplicated.
func whenModified(u *engine.Unit, byPath map[string]*engine.Unit, repoRoot string, extraDeps []string, opts Options) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	rel := func(target string) string {
		r, err := filepath.Rel(u.Path, target)
		if err != nil {
			return ""
		}
		return filepath.ToSlash(r)
	}

	// The unit's own directory: any hcl/tf change triggers it.
	add("*.hcl")
	add("*.tf*")

	// extra_atlantis_dependencies locals: made relative to the unit dir.
	// They evaluate to absolute paths when built from get_terragrunt_dir().
	absUnitDir := filepath.Join(repoRoot, u.Path)
	for _, d := range extraDeps {
		if filepath.IsAbs(d) {
			if r, err := filepath.Rel(absUnitDir, d); err == nil {
				add(filepath.ToSlash(r))
			}
			continue
		}
		add(filepath.ToSlash(filepath.Clean(d)))
	}

	// addSource turns a source file into a when_modified entry, matching
	// terragrunt-atlantis-config: terraform files collapse to a `<dir>/*.tf*`
	// glob (covering .tf/.tf.json/.tfvars/.tftpl and files added later);
	// config includes (.hcl) are kept concrete; module data files referenced
	// via file()/templatefile() (.json, .tpl, .sh, ...) are not listed.
	addSource := func(sf string) {
		base := filepath.Base(sf)
		r := rel(sf)
		if r == "" {
			return
		}
		if strings.Contains(base, ".tf") {
			dir := pathDir(r)
			if dir == "." {
				add("*.tf*")
			} else {
				add(dir + "/*.tf*")
			}
			return
		}
		if strings.HasSuffix(base, ".hcl") {
			add(r)
		}
	}

	// The unit plus its dependency closure.
	for _, dep := range dependencyClosure(u, byPath, opts) {
		d := byPath[dep]
		if d == nil {
			continue
		}
		if d.Path != u.Path {
			add(rel(filepath.Join(d.Path, "terragrunt.hcl")))
		}
		for _, sf := range d.SourceFiles {
			addSource(sf)
		}
	}

	return out
}

// pathDir is filepath.Dir on a slash path, returning "." for a bare filename.
func pathDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

// dependencyClosure returns u plus its dependencies (transitive when cascade
// is enabled), restricted to units present in the graph.
func dependencyClosure(u *engine.Unit, byPath map[string]*engine.Unit, opts Options) []string {
	closure := []string{u.Path}
	seen := map[string]bool{u.Path: true}
	queue := []*engine.Unit{u}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if opts.IgnoreDependencyBlocks {
			continue
		}
		for _, dep := range cur.Deps {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			closure = append(closure, dep)
			if d := byPath[dep]; d != nil && opts.CascadeDependencies {
				queue = append(queue, d)
			}
		}
	}
	return closure
}

// assignExecutionOrder sets execution_order_group (and depends_on) from the
// dependency DAG: a project's group is one past the max of its dependencies'.
func assignExecutionOrder(projects []Project, byPath map[string]*engine.Unit, emitted map[string]bool, opts Options) {
	group := map[string]int{}
	var compute func(path string, stack map[string]bool) int
	compute = func(path string, stack map[string]bool) int {
		if g, ok := group[path]; ok {
			return g
		}
		if stack[path] {
			return 0 // cycle guard
		}
		stack[path] = true
		defer delete(stack, path)

		g := 0
		if u := byPath[path]; u != nil {
			for _, dep := range u.Deps {
				if !emitted[dep] {
					continue
				}
				if dg := compute(dep, stack) + 1; dg > g {
					g = dg
				}
			}
		}
		group[path] = g
		return g
	}

	for i := range projects {
		g := compute(projects[i].Dir, map[string]bool{})
		if opts.ExecutionOrderGroups {
			projects[i].ExecutionOrderGroup = &g
		}
		if opts.DependsOn {
			projects[i].DependsOn = dependsOnNames(projects[i].Dir, byPath, emitted)
		}
	}

	if opts.ExecutionOrderGroups {
		sort.SliceStable(projects, func(i, j int) bool {
			gi, gj := *projects[i].ExecutionOrderGroup, *projects[j].ExecutionOrderGroup
			if gi == gj {
				return projects[i].Dir < projects[j].Dir
			}
			return gi < gj
		})
	}
}

func dependsOnNames(dir string, byPath map[string]*engine.Unit, emitted map[string]bool) []string {
	u := byPath[dir]
	if u == nil {
		return nil
	}
	var names []string
	for _, dep := range u.Deps {
		if emitted[dep] {
			names = append(names, sanitizeName(filepath.ToSlash(dep)))
		}
	}
	slices.Sort(names)
	return names
}

// matchesFilter reports whether path equals, is under, or glob-matches any
// of the filter entries (paths relative to the repo root).
func matchesFilter(path string, filters []string) bool {
	for _, f := range filters {
		f = filepath.Clean(f)
		if path == f {
			return true
		}
		if rel, err := filepath.Rel(f, path); err == nil && rel != ".." && !startsWithDotDot(rel) {
			return true
		}
		if ok, _ := filepath.Match(f, path); ok {
			return true
		}
	}
	return false
}

func startsWithDotDot(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}
