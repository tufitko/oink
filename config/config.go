// Package config — loading, merging and validation of the pipeline config
// (.tgops.yaml).
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Pipeline is the config root: the main pipeline plus finally.
type Pipeline struct {
	Pipeline []Operation `yaml:"pipeline"`
	Finally  []Operation `yaml:"finally"`
}

// Operation is a single pipeline operation: which registered operation to
// run (use), under what display name, with what parameters and what failure
// policy. Operation-specific parameters live entirely in With — exec is an
// ordinary operation configured via with.run like any other.
type Operation struct {
	Use       string    `yaml:"use"`
	Name      string    `yaml:"name"`
	With      yaml.Node `yaml:"with"` // yaml.v3 decodes Node by value only
	OnFailure OnFailure `yaml:"on_failure"`
}

// DisplayName is the operation name used in events and reports.
func (o Operation) DisplayName() string {
	if o.Name != "" {
		return o.Name
	}
	return o.Use
}

// DecodeWith decodes the operation's with-parameters into a typed struct.
func (o Operation) DecodeWith(out any) error {
	if o.With.IsZero() {
		return nil
	}
	return o.With.Decode(out)
}

// UnitPipeline extracts the nested pipeline from with.pipeline (for
// operations whose metadata declares UnitPipeline=true, e.g. builtin units).
func (o Operation) UnitPipeline() ([]Operation, error) {
	var params struct {
		Pipeline []Operation `yaml:"pipeline"`
	}
	if err := o.DecodeWith(&params); err != nil {
		return nil, err
	}
	return params.Pipeline, nil
}

// Load reads and parses the YAML config from a file.
func Load(path string) (*Pipeline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse parses the YAML config.
func Parse(raw []byte) (*Pipeline, error) {
	var p Pipeline
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &p, nil
}

// Merge overlays the repo config on top of server-side defaults. At this
// stage the server layer is empty; semantics — the repo config fully
// replaces the corresponding defaults section.
func Merge(server, repo *Pipeline) *Pipeline {
	switch {
	case server == nil && repo == nil:
		return &Pipeline{}
	case server == nil:
		return repo
	case repo == nil:
		return server
	}
	out := &Pipeline{Pipeline: server.Pipeline, Finally: server.Finally}
	if len(repo.Pipeline) > 0 {
		out.Pipeline = repo.Pipeline
	}
	if len(repo.Finally) > 0 {
		out.Finally = repo.Finally
	}
	return out
}
