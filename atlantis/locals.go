package atlantis

import "github.com/zclconf/go-cty/cty"

// unitLocals are the atlantis_* locals terragrunt-atlantis-config recognises,
// read from a unit's own locals block. Inheritance through include blocks is
// not resolved (see discovery.Collector) — values must be set on the unit.
type unitLocals struct {
	Workflow          string
	TerraformVersion  string
	ApplyRequirements *[]string
	SilencePRComments *[]string
	ExtraDeps         []string
	AutoPlan          *bool
	Skip              bool
	MarkedProject     bool // `atlantis_project` present → an explicit project marker
}

// resolveLocals extracts the atlantis_* locals from an evaluated locals map.
func resolveLocals(vals map[string]cty.Value) unitLocals {
	var l unitLocals

	if v, ok := stringVal(vals, "atlantis_workflow"); ok {
		l.Workflow = v
	}
	if v, ok := stringVal(vals, "atlantis_terraform_version"); ok {
		l.TerraformVersion = v
	}
	if v, ok := boolVal(vals, "atlantis_autoplan"); ok {
		l.AutoPlan = &v
	}
	if v, ok := boolVal(vals, "atlantis_skip"); ok {
		l.Skip = v
	}
	if _, ok := vals["atlantis_project"]; ok {
		l.MarkedProject = true
	}
	if v, ok := stringSliceVal(vals, "atlantis_apply_requirements"); ok {
		l.ApplyRequirements = &v
	}
	if v, ok := stringSliceVal(vals, "atlantis_silence_pr_comments"); ok {
		l.SilencePRComments = &v
	}
	if v, ok := stringSliceVal(vals, "extra_atlantis_dependencies"); ok {
		l.ExtraDeps = v
	}
	return l
}

func stringVal(vals map[string]cty.Value, key string) (string, bool) {
	v, ok := vals[key]
	if !ok || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

func boolVal(vals map[string]cty.Value, key string) (bool, bool) {
	v, ok := vals[key]
	if !ok || v.IsNull() || !v.IsKnown() || v.Type() != cty.Bool {
		return false, false
	}
	return v.True(), true
}

// stringSliceVal reads a list/tuple of strings; non-string elements are skipped.
func stringSliceVal(vals map[string]cty.Value, key string) ([]string, bool) {
	v, ok := vals[key]
	if !ok || v.IsNull() || !v.IsKnown() || !v.CanIterateElements() {
		return nil, false
	}
	out := []string{}
	for _, el := range v.AsValueSlice() {
		if el.IsKnown() && !el.IsNull() && el.Type() == cty.String {
			out = append(out, el.AsString())
		}
	}
	return out, true
}
