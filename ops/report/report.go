// Package report — builtin reporter: a human-readable units×operations table
// and run.json on disk. Works off the unit_results aggregate in the context.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"text/tabwriter"

	"github.com/tufitko/oink/engine"
	"github.com/tufitko/oink/ops/units"
)

// DefaultPath is the default run.json name (relative to repo root).
const DefaultPath = "run.json"

// Params are the with-parameters of report.
type Params struct {
	Path string `yaml:"path"` // run.json path; relative paths resolve from repo root
}

type Op struct {
	path string
	out  io.Writer
}

// New creates a report operation. path == "" → DefaultPath; out == nil → os.Stdout.
func New(path string, out io.Writer) *Op {
	if path == "" {
		path = DefaultPath
	}
	if out == nil {
		out = os.Stdout
	}
	return &Op{path: path, out: out}
}

func (o *Op) Name() string { return "report" }

// runJSON is the run.json schema.
type runJSON struct {
	RunID   string     `json:"run_id"`
	Repo    string     `json:"repo,omitempty"`
	PR      int        `json:"pr,omitempty"`
	SHA     string     `json:"sha,omitempty"`
	Command string     `json:"command"`
	Units   []unitJSON `json:"units"`
}

type unitJSON struct {
	Path   string   `json:"path"`
	Status string   `json:"status"`
	Ops    []opJSON `json:"ops,omitempty"`
}

type opJSON struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (o *Op) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	results, ok := engine.Get(rc.Vars, units.KeyResults)
	if !ok {
		// The pipeline may have terminated before units — still report.
		results = map[string]*units.UnitResult{}
	}

	sorted := sortedResults(results)
	o.printTable(sorted)

	if err := o.writeJSON(rc, sorted); err != nil {
		return engine.Result{Status: engine.StatusFailed, Err: err}
	}
	return engine.Result{Status: engine.StatusOK}
}

func sortedResults(results map[string]*units.UnitResult) []*units.UnitResult {
	out := make([]*units.UnitResult, 0, len(results))
	for _, ur := range results {
		out = append(out, ur)
	}
	slices.SortFunc(out, func(a, b *units.UnitResult) int {
		if a.Unit.Path < b.Unit.Path {
			return -1
		}
		if a.Unit.Path > b.Unit.Path {
			return 1
		}
		return 0
	})
	return out
}

// opColumns is the union of operation names across all units, order-preserving.
func opColumns(sorted []*units.UnitResult) []string {
	var cols []string
	seen := map[string]bool{}
	for _, ur := range sorted {
		for _, op := range ur.Ops {
			if !seen[op.Name] {
				seen[op.Name] = true
				cols = append(cols, op.Name)
			}
		}
	}
	return cols
}

func (o *Op) printTable(sorted []*units.UnitResult) {
	if len(sorted) == 0 {
		fmt.Fprintln(o.out, "report: no unit results (units did not run)")
		return
	}

	cols := opColumns(sorted)
	w := tabwriter.NewWriter(o.out, 2, 4, 2, ' ', 0)
	header := "UNIT\tSTATUS"
	for _, c := range cols {
		header += "\t" + c
	}
	fmt.Fprintln(w, header)

	counts := map[engine.Status]int{}
	for _, ur := range sorted {
		counts[ur.Status]++
		row := ur.Unit.Path + "\t" + ur.Status.String()
		byName := map[string]string{}
		for _, op := range ur.Ops {
			byName[op.Name] = op.Result.Status.String()
		}
		for _, c := range cols {
			cell, ok := byName[c]
			if !ok {
				cell = "-"
			}
			row += "\t" + cell
		}
		fmt.Fprintln(w, row)
	}
	w.Flush()

	fmt.Fprintf(o.out, "\nTOTAL: %d units — %d ok, %d warn, %d failed, %d skipped\n",
		len(sorted),
		counts[engine.StatusOK], counts[engine.StatusWarn],
		counts[engine.StatusFailed], counts[engine.StatusSkipped])
}

func (o *Op) writeJSON(rc *engine.RunContext, sorted []*units.UnitResult) error {
	doc := runJSON{
		RunID:   rc.Run.RunID,
		Repo:    rc.Run.Repo,
		PR:      rc.Run.PR,
		SHA:     rc.Run.SHA,
		Command: string(rc.Run.Command),
		Units:   []unitJSON{},
	}
	for _, ur := range sorted {
		uj := unitJSON{Path: ur.Unit.Path, Status: ur.Status.String()}
		for _, op := range ur.Ops {
			oj := opJSON{Name: op.Name, Status: op.Result.Status.String(), ExitCode: op.Result.ExitCode}
			if op.Result.Err != nil {
				oj.Error = op.Result.Err.Error()
			}
			uj.Ops = append(uj.Ops, oj)
		}
		doc.Units = append(doc.Units, uj)
	}

	path := o.path
	if !filepath.IsAbs(path) && rc.Run.RepoRoot != "" {
		path = filepath.Join(rc.Run.RepoRoot, path)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run.json: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write run.json: %w", err)
	}
	return nil
}
