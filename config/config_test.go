package config

import (
	"strings"
	"testing"
)

// fakeRegistry mirrors the metadata of the stage-1 builtins.
type fakeRegistry map[string]OpMeta

func (r fakeRegistry) Meta(use string) (OpMeta, bool) {
	m, ok := r[use]
	return m, ok
}

func testRegistry() fakeRegistry {
	return fakeRegistry{
		"discovery":  {Run: true, Provides: []string{"units"}},
		"units":      {Run: true, Requires: []string{"units"}, Provides: []string{"unit_results"}, UnitPipeline: true},
		"exec":       {Run: true, Unit: true},
		"terragrunt": {Unit: true},
		"report":     {Run: true},
	}
}

const validYAML = `
pipeline:
  - use: discovery
  - use: exec
    name: hello
    with: { run: 'echo "run {{ .Run.RunID }}: {{ .UnitsCount }} units affected"' }
    on_failure: continue
  - use: units
    with:
      parallelism: 8
      order: dag
      pipeline:
        - use: exec
          name: unit-hello
          with: { run: 'echo "processing {{ .Unit.Path }}"' }
        - use: terragrunt
          with: { command: plan }
        - use: exec
          name: after
          with: { run: 'echo "done {{ .Unit.Path }}, exit={{ .Prev.ExitCode }}"' }
          on_failure: continue
finally:
  - use: report
`

func TestParseAndValidateValid(t *testing.T) {
	p, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pipeline) != 3 || len(p.Finally) != 1 {
		t.Fatalf("got %d pipeline ops, %d finally ops; want 3 and 1", len(p.Pipeline), len(p.Finally))
	}
	if err := Validate(p, testRegistry()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	nested, err := p.Pipeline[2].UnitPipeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 3 {
		t.Fatalf("got %d nested ops, want 3", len(nested))
	}
	if nested[1].Use != "terragrunt" {
		t.Fatalf("nested[1] use = %q", nested[1].Use)
	}
}

func validate(t *testing.T, yaml string) error {
	t.Helper()
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return Validate(p, testRegistry())
}

func TestValidateUnknownUse(t *testing.T) {
	err := validate(t, `
pipeline:
  - use: discovery
  - use: nosuch
`)
	if err == nil || !strings.Contains(err.Error(), `pipeline[1]`) || !strings.Contains(err.Error(), "nosuch") {
		t.Fatalf("want error mentioning pipeline[1] and nosuch, got: %v", err)
	}
}

func TestValidateRunOpInsideUnits(t *testing.T) {
	err := validate(t, `
pipeline:
  - use: discovery
  - use: units
    with:
      pipeline:
        - use: report
`)
	if err == nil || !strings.Contains(err.Error(), "pipeline[1].with.pipeline[0]") {
		t.Fatalf("want error with nested path, got: %v", err)
	}
}

func TestValidateRequiresProvides(t *testing.T) {
	err := validate(t, `
pipeline:
  - use: units
    with:
      pipeline:
        - use: exec
          with: { run: 'echo hi' }
`)
	if err == nil || !strings.Contains(err.Error(), `"units"`) || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("want requires/provides violation, got: %v", err)
	}
}

func TestValidateMissingUse(t *testing.T) {
	err := validate(t, `
pipeline:
  - name: empty
`)
	if err == nil || !strings.Contains(err.Error(), "pipeline[0]") || !strings.Contains(err.Error(), "'use' is required") {
		t.Fatalf("want missing-use error for pipeline[0], got: %v", err)
	}
}

func TestValidateBadOnFailure(t *testing.T) {
	err := validate(t, `
pipeline:
  - use: exec
    with: { run: 'echo hi' }
    on_failure: retry
`)
	if err == nil || !strings.Contains(err.Error(), "on_failure") {
		t.Fatalf("want on_failure error, got: %v", err)
	}
}

func TestValidateUnitOpOnTopLevel(t *testing.T) {
	err := validate(t, `
pipeline:
  - use: terragrunt
`)
	if err == nil || !strings.Contains(err.Error(), "top level") {
		t.Fatalf("want top-level interface error, got: %v", err)
	}
}

func TestMerge(t *testing.T) {
	repo := &Pipeline{Pipeline: []Operation{{Use: "discovery"}}}
	server := &Pipeline{
		Pipeline: []Operation{{Use: "exec"}},
		Finally:  []Operation{{Use: "report"}},
	}

	merged := Merge(server, repo)
	if len(merged.Pipeline) != 1 || merged.Pipeline[0].Use != "discovery" {
		t.Fatalf("repo pipeline must win: %+v", merged.Pipeline)
	}
	if len(merged.Finally) != 1 || merged.Finally[0].Use != "report" {
		t.Fatalf("server finally must be kept: %+v", merged.Finally)
	}

	if got := Merge(nil, repo); got != repo {
		t.Fatal("Merge(nil, repo) must return repo")
	}
	if got := Merge(server, nil); got != server {
		t.Fatal("Merge(server, nil) must return server")
	}
}
