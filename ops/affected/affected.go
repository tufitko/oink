// Package affected — builtin affected: narrows the unit graph in the context
// to the units from Run.ChangedDirs plus their transitive dependents.
// Optional: a pipeline without it runs over all discovered units.
package affected

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tufitko/oink/engine"
)

type Op struct{}

func New() *Op { return &Op{} }

func (o *Op) Name() string { return "affected" }

func (o *Op) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	g := rc.Units
	if g == nil {
		return engine.Result{Status: engine.StatusFailed, Err: fmt.Errorf("unit graph is not in context: discovery must run before affected")}
	}

	total := len(g.All())
	narrowed, err := Narrow(g, rc.Run.ChangedDirs)
	if err != nil {
		return engine.Result{Status: engine.StatusFailed, Err: err}
	}

	rc.Units = narrowed
	return engine.Result{Status: engine.StatusOK, Stdout: fmt.Sprintf("%d units affected (of %d discovered)\n", len(narrowed.All()), total)}
}

// Narrow returns the subgraph: units from changedDirs plus their transitive
// dependents; dependencies are pruned to the units remaining in the subgraph.
// An empty changedDirs means "all units" — the graph is returned as is.
func Narrow(g *engine.UnitGraph, changedDirs []string) (*engine.UnitGraph, error) {
	if len(changedDirs) == 0 {
		return g, nil
	}

	include := map[string]bool{}
	for _, dir := range changedDirs {
		u, ok := g.ByPath(filepath.Clean(dir))
		if !ok {
			continue // the changed dir is not a unit (e.g. a shared module)
		}
		include[u.Path] = true
		for _, dep := range g.Dependents(u) {
			include[dep.Path] = true
		}
	}

	var subset []*engine.Unit
	for _, u := range g.All() {
		if !include[u.Path] {
			continue
		}
		var deps []string
		for _, dep := range u.Deps {
			if include[dep] {
				deps = append(deps, dep)
			}
		}
		subset = append(subset, &engine.Unit{Path: u.Path, Deps: deps, SourceFiles: u.SourceFiles})
	}
	return engine.NewUnitGraph(subset)
}
