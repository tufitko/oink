// Package atlantis generates an Atlantis configuration (atlantis.yaml) from a
// terragrunt repository, as a functional equivalent of
// `terragrunt-atlantis-config generate`. It reuses oink's discovery (unit
// graph + per-unit source files) instead of re-implementing terragrunt
// parsing.
//
// The output is functionally equivalent, not byte-identical: one project per
// discovered unit, autoplan.when_modified covering the unit's own files, its
// dependencies' configs and (with cascade) transitive dependency files. Some
// when_modified entries are concrete module files where terragrunt-atlantis-config
// emits `<dir>/*.tf*` globs.
package atlantis

import (
	"os"
	"regexp"

	"sigs.k8s.io/yaml"
)

// Config is the atlantis.yaml document. JSON tags drive YAML marshaling
// (sigs.k8s.io/yaml encodes via JSON and sorts keys), matching the layout of
// terragrunt-atlantis-config.
type Config struct {
	Version       int       `json:"version"`
	AutoMerge     bool      `json:"automerge"`
	ParallelPlan  bool      `json:"parallel_plan"`
	ParallelApply bool      `json:"parallel_apply"`
	Projects      []Project `json:"projects,omitempty"`
	// Workflows is preserved verbatim from an existing output file; this
	// generator does not manage workflow definitions.
	Workflows any `json:"workflows,omitempty"`
}

// Project is one Atlantis project (one terragrunt unit directory).
type Project struct {
	Dir                 string     `json:"dir"`
	Workflow            string     `json:"workflow,omitempty"`
	Workspace           string     `json:"workspace,omitempty"`
	Name                string     `json:"name,omitempty"`
	Autoplan            Autoplan   `json:"autoplan"`
	TerraformVersion    string     `json:"terraform_version,omitempty"`
	ApplyRequirements   *[]string  `json:"apply_requirements,omitempty"`
	ExecutionOrderGroup *int       `json:"execution_order_group,omitempty"`
	DependsOn           []string   `json:"depends_on,omitempty"`
	SilencePRComments   []string   `json:"silence_pr_comments,omitempty"`
	RepoLocks           *RepoLocks `json:"repo_locks,omitempty"`
}

// Autoplan controls when Atlantis auto-plans a project.
type Autoplan struct {
	WhenModified []string `json:"when_modified"`
	Enabled      bool     `json:"enabled"`
}

// RepoLocks controls repository locking on plan/apply.
type RepoLocks struct {
	Mode string `json:"mode"`
}

// Marshal renders the config as atlantis.yaml bytes.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}

// projectNameRe mirrors terragrunt-atlantis-config: workspace/name are the
// project dir with everything outside [a-zA-Z0-9_-] replaced by `_`.
var projectNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeName(dir string) string {
	return projectNameRe.ReplaceAllString(dir, "_")
}

// readWorkflows returns the `workflows:` block of an existing atlantis config,
// or nil if the file is absent. A malformed file is an error.
func readWorkflows(path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var old Config
	if err := yaml.Unmarshal(raw, &old); err != nil {
		return nil, err
	}
	return old.Workflows, nil
}
