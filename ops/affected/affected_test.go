package affected

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tufitko/oink/engine"
)

func testGraph(t *testing.T) *engine.UnitGraph {
	t.Helper()
	g, err := engine.NewUnitGraph([]*engine.Unit{
		{Path: "net"},
		{Path: "db", Deps: []string{"net"}, SourceFiles: []string{"modules/storage/main.tf"}},
		{Path: "app", Deps: []string{"db"}},
		{Path: "worker", Deps: []string{"db"}},
		{Path: "standalone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func newRC(t *testing.T, changedDirs ...string) *engine.RunContext {
	return &engine.RunContext{
		Run:    engine.RunInfo{RunID: "r1", ChangedDirs: changedDirs},
		Units:  testGraph(t),
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

func TestEmptyChangedDirsKeepsAllUnits(t *testing.T) {
	rc := newRC(t)
	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	want := []string{"app", "db", "net", "standalone", "worker"}
	if got := graphPaths(rc.Units); !slices.Equal(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
}

func TestTransitiveDependents(t *testing.T) {
	rc := newRC(t, "db")
	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	// db changed → db plus transitive dependents app and worker; net and standalone are out.
	want := []string{"app", "db", "worker"}
	if got := graphPaths(rc.Units); !slices.Equal(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
	// The db → net dependency is pruned: net is outside the subgraph.
	db, _ := rc.Units.ByPath("db")
	if len(db.Deps) != 0 {
		t.Fatalf("db deps = %v, want pruned to empty", db.Deps)
	}
	// app → db is kept.
	app, _ := rc.Units.ByPath("app")
	if !slices.Equal(app.Deps, []string{"db"}) {
		t.Fatalf("app deps = %v, want [db]", app.Deps)
	}
	// SourceFiles survive the narrowing.
	if !slices.Equal(db.SourceFiles, []string{"modules/storage/main.tf"}) {
		t.Fatalf("db source files lost in Narrow: %v", db.SourceFiles)
	}
	if !strings.Contains(res.Stdout, "3 units affected (of 5 discovered)") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestChangedDirNotAUnit(t *testing.T) {
	rc := newRC(t, "modules/shared")
	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	if got := len(rc.Units.All()); got != 0 {
		t.Fatalf("units = %d, want 0 for non-unit changed dir", got)
	}
}

func TestChangedDirPathNormalized(t *testing.T) {
	rc := newRC(t, "./db/")
	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusOK {
		t.Fatalf("status = %v, err = %v", res.Status, res.Err)
	}
	want := []string{"app", "db", "worker"}
	if got := graphPaths(rc.Units); !slices.Equal(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
}

func TestNoGraphInContext(t *testing.T) {
	rc := &engine.RunContext{
		Run:    engine.RunInfo{RunID: "r1"},
		Events: engine.NopSink{},
		Vars:   engine.NewVars(),
	}
	res := New().Run(context.Background(), rc)
	if res.Status != engine.StatusFailed || res.Err == nil {
		t.Fatalf("want failure without unit graph, got %v / %v", res.Status, res.Err)
	}
}
