// Package discovery — builtin discovery: builds the full UnitGraph via
// `terragrunt find --dag --json` and puts it into the context (provides:
// units). Narrowing the graph to the affected set is a separate builtin
// (ops/affected).
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/tufitko/oink/engine"
)

// KeyName is the unit-graph key name for requires/provides metadata.
const KeyName = "units"

type Op struct{}

func New() *Op { return &Op{} }

func (o *Op) Name() string { return "discovery" }

func (o *Op) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	units, err := Discover(ctx, rc.Run.RepoRoot)
	if err != nil {
		return engine.Result{Status: engine.StatusFailed, Err: err}
	}

	g, err := engine.NewUnitGraph(units)
	if err != nil {
		return engine.Result{Status: engine.StatusFailed, Err: err}
	}

	rc.Units = g
	return engine.Result{Status: engine.StatusOK, Stdout: fmt.Sprintf("%d units discovered\n", len(units))}
}

// Discover runs `terragrunt find` under repoRoot and returns every unit with
// its direct dependencies and SourceFiles populated. It is the standalone
// entry point reused outside the pipeline (e.g. generate-atlantis-config).
func Discover(ctx context.Context, repoRoot string) ([]*engine.Unit, error) {
	units, reading, err := terragruntFind(ctx, repoRoot)
	if err != nil {
		return nil, err
	}

	collector, err := NewCollector(repoRoot)
	if err != nil {
		return nil, err
	}
	// Partial parse costs ~25ms per unit — resolve sources in parallel.
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for _, u := range units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u.SourceFiles = collector.SourceFiles(u.Path, reading[u.Path])
		}()
	}
	wg.Wait()
	return units, nil
}

// terragruntFind invokes `terragrunt find --dag --dependencies --reading
// --json`. It returns the units and the per-unit list of files their configs
// read (paths relative to repoRoot). The output format and flag names match
// terragrunt >= 0.73 (CLI redesign); the version is pinned in the README.
func terragruntFind(ctx context.Context, repoRoot string) ([]*engine.Unit, map[string][]string, error) {
	cmd := exec.CommandContext(ctx, "terragrunt", "find", "--dag", "--dependencies", "--reading", "--json")
	cmd.Dir = repoRoot
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("terragrunt find: %w: %s", err, errBuf.String())
	}

	var found []struct {
		Type         string   `json:"type"`
		Path         string   `json:"path"`
		Dependencies []string `json:"dependencies"`
		Reading      []string `json:"reading"`
	}
	if err := json.Unmarshal(out.Bytes(), &found); err != nil {
		return nil, nil, fmt.Errorf("parse terragrunt find output: %w", err)
	}
	var units []*engine.Unit
	reading := map[string][]string{}
	for _, f := range found {
		if f.Type != "" && f.Type != "unit" {
			continue
		}
		u := &engine.Unit{Path: f.Path, Deps: f.Dependencies}
		units = append(units, u)
		if len(f.Reading) > 0 {
			reading[filepath.Clean(f.Path)] = f.Reading
		}
	}
	return normalize(units), reading, nil
}

func normalize(units []*engine.Unit) []*engine.Unit {
	for _, u := range units {
		u.Path = filepath.Clean(u.Path)
		for i, dep := range u.Deps {
			u.Deps[i] = filepath.Clean(dep)
		}
	}
	return units
}
