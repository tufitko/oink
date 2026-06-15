package engine

import (
	"slices"
	"testing"
)

// testGraph:
//
//	net <- db <- app
//	       db <- worker
//	standalone
func testGraph(t *testing.T) *UnitGraph {
	t.Helper()
	g, err := NewUnitGraph([]*Unit{
		{Path: "net"},
		{Path: "db", Deps: []string{"net"}},
		{Path: "app", Deps: []string{"db"}},
		{Path: "worker", Deps: []string{"db"}},
		{Path: "standalone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func paths(units []*Unit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.Path
	}
	slices.Sort(out)
	return out
}

func TestNewUnitGraphDuplicate(t *testing.T) {
	_, err := NewUnitGraph([]*Unit{{Path: "a"}, {Path: "a"}})
	if err == nil {
		t.Fatal("expected error on duplicate unit path")
	}
}

func TestDependentsTransitive(t *testing.T) {
	g := testGraph(t)
	net, _ := g.ByPath("net")
	got := paths(g.Dependents(net))
	want := []string{"app", "db", "worker"}
	if !slices.Equal(got, want) {
		t.Fatalf("Dependents(net) = %v, want %v", got, want)
	}

	app, _ := g.ByPath("app")
	if deps := g.Dependents(app); len(deps) != 0 {
		t.Fatalf("Dependents(app) = %v, want empty", paths(deps))
	}
}

func TestTopoBatches(t *testing.T) {
	g := testGraph(t)
	batches, err := g.TopoBatches()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"net", "standalone"},
		{"db"},
		{"app", "worker"},
	}
	if len(batches) != len(want) {
		t.Fatalf("got %d batches, want %d", len(batches), len(want))
	}
	for i := range want {
		if got := paths(batches[i]); !slices.Equal(got, want[i]) {
			t.Errorf("batch %d = %v, want %v", i, got, want[i])
		}
	}
}

func TestTopoBatchesExternalDepsSatisfied(t *testing.T) {
	// A dependency on a unit outside the graph (excluded from the affected
	// set) is considered satisfied and must not push the unit to a later wave.
	g, err := NewUnitGraph([]*Unit{
		{Path: "app", Deps: []string{"outside"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	batches, err := g.TopoBatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("got %v, want one batch with one unit", batches)
	}
}

func TestTopoBatchesCycle(t *testing.T) {
	g, err := NewUnitGraph([]*Unit{
		{Path: "a", Deps: []string{"b"}},
		{Path: "b", Deps: []string{"a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.TopoBatches(); err == nil {
		t.Fatal("expected cycle error")
	}
}
