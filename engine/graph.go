package engine

import "fmt"

// UnitGraph is the unit DAG. Dependencies pointing outside the graph
// (units excluded from the affected set) are considered satisfied.
type UnitGraph struct {
	units  []*Unit
	byPath map[string]*Unit
}

func NewUnitGraph(units []*Unit) (*UnitGraph, error) {
	byPath := make(map[string]*Unit, len(units))
	for _, u := range units {
		if _, dup := byPath[u.Path]; dup {
			return nil, fmt.Errorf("duplicate unit path %q", u.Path)
		}
		byPath[u.Path] = u
	}
	return &UnitGraph{units: units, byPath: byPath}, nil
}

func (g *UnitGraph) All() []*Unit { return g.units }

func (g *UnitGraph) ByPath(path string) (*Unit, bool) {
	u, ok := g.byPath[path]
	return u, ok
}

// Dependents returns all units transitively depending on u.
func (g *UnitGraph) Dependents(u *Unit) []*Unit {
	reverse := make(map[string][]*Unit) // path -> direct dependents
	for _, candidate := range g.units {
		for _, dep := range candidate.Deps {
			reverse[dep] = append(reverse[dep], candidate)
		}
	}

	seen := map[string]bool{u.Path: true}
	var out []*Unit
	queue := []*Unit{u}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dependent := range reverse[cur.Path] {
			if seen[dependent.Path] {
				continue
			}
			seen[dependent.Path] = true
			out = append(out, dependent)
			queue = append(queue, dependent)
		}
	}
	return out
}

// TopoBatches returns topological "waves" for order: dag — units within one
// wave do not depend on each other and can run in parallel.
func (g *UnitGraph) TopoBatches() ([][]*Unit, error) {
	indegree := make(map[string]int, len(g.units))
	for _, u := range g.units {
		for _, dep := range u.Deps {
			if _, inGraph := g.byPath[dep]; inGraph {
				indegree[u.Path]++
			}
		}
	}

	var batches [][]*Unit
	remaining := make(map[string]*Unit, len(g.units))
	for _, u := range g.units {
		remaining[u.Path] = u
	}

	for len(remaining) > 0 {
		var batch []*Unit
		for _, u := range g.units {
			if _, left := remaining[u.Path]; left && indegree[u.Path] == 0 {
				batch = append(batch, u)
			}
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("dependency cycle among %d remaining units", len(remaining))
		}
		for _, u := range batch {
			delete(remaining, u.Path)
		}
		for _, u := range batch {
			for _, dependent := range g.units {
				if _, left := remaining[dependent.Path]; !left {
					continue
				}
				for _, dep := range dependent.Deps {
					if dep == u.Path {
						indegree[dependent.Path]--
					}
				}
			}
		}
		batches = append(batches, batch)
	}
	return batches, nil
}
