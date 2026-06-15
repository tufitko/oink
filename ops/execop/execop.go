// Package execop — builtin exec: runs a shell command rendered as a
// text/template from the run or unit context. Implements both operation
// interfaces.
package execop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/tufitko/oink/engine"
)

const (
	// DefaultTimeout is the default command timeout.
	DefaultTimeout = 10 * time.Minute
	// MaxOutput caps the size of the captured output (Result.Stdout).
	MaxOutput = 64 * 1024
)

// Params are the with-parameters of the exec operation.
type Params struct {
	Timeout time.Duration `yaml:"timeout"`
}

type Op struct {
	name    string
	command string
	timeout time.Duration
}

// New creates an exec operation. name is the display name (the config's
// name field), command is the command template, timeout <= 0 falls back to
// the default.
func New(name, command string, timeout time.Duration) *Op {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Op{name: name, command: command, timeout: timeout}
}

func (o *Op) Name() string { return o.name }

// runData — template variables at the top level.
type runData struct {
	Run        engine.RunInfo
	UnitsCount int
}

// unitData — template variables inside the unit pipeline.
type unitData struct {
	Run        engine.RunInfo
	UnitsCount int
	Unit       *engine.Unit
	Prev       *engine.Result
}

func (o *Op) Run(ctx context.Context, rc *engine.RunContext) engine.Result {
	return o.exec(ctx, rc, runData{Run: rc.Run, UnitsCount: rc.UnitsCount()})
}

func (o *Op) RunUnit(ctx context.Context, uc *engine.UnitContext) engine.Result {
	rc := uc.Run
	return o.exec(ctx, rc, unitData{
		Run:        rc.Run,
		UnitsCount: rc.UnitsCount(),
		Unit:       uc.Unit,
		Prev:       uc.Prev,
	})
}

func (o *Op) exec(ctx context.Context, rc *engine.RunContext, data any) engine.Result {
	rendered, err := renderCommand(o.command, data)
	if err != nil {
		return engine.Result{Status: engine.StatusFailed, ExitCode: -1, Err: fmt.Errorf("render command: %w", err)}
	}

	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", rendered)
	if rc.Run.RepoRoot != "" {
		cmd.Dir = rc.Run.RepoRoot
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	stdout := truncate(out.String(), MaxOutput)

	res := engine.Result{Status: engine.StatusOK, Stdout: stdout}
	if runErr != nil {
		res.Status = engine.StatusFailed
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			res.Err = fmt.Errorf("command timed out after %s: %w", o.timeout, runErr)
		} else {
			res.Err = runErr
		}
	}
	return res
}

func renderCommand(tmpl string, data any) (string, error) {
	t, err := template.New("run").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n...[truncated]"
}
