package engine

// Status is the outcome of an operation (and of aggregates: unit, run).
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFailed
	StatusSkipped
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Worse returns the worse of the two statuses for aggregation.
// Degradation order: ok < skipped < warn < failed.
func (s Status) Worse(other Status) Status {
	rank := func(s Status) int {
		switch s {
		case StatusOK:
			return 0
		case StatusSkipped:
			return 1
		case StatusWarn:
			return 2
		case StatusFailed:
			return 3
		}
		return 0
	}
	if rank(other) > rank(s) {
		return other
	}
	return s
}

// Result is the outcome of a single operation.
type Result struct {
	Status   Status
	ExitCode int
	Err      error
	Changes  *PlanChanges // filled by the terragrunt operation (show -json); may be nil for now
	Stdout   string       // will become ArtifactRef later; for now a size-capped string
}

// PlanChanges is a terraform plan change summary.
type PlanChanges struct{ Add, Change, Destroy int }
