// Package terragrunt — builtin unit operation: per-unit terragrunt CLI
// invocation. run-all is not used: the wrapper itself is the DAG scheduler,
// and a standalone `terragrunt plan` resolves dependency blocks on its own.
package terragrunt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/tufitko/oink/engine"
)

// DefaultTimeout is the default timeout of a per-unit terragrunt command.
const DefaultTimeout = 30 * time.Minute

// Params are the with-parameters of the terragrunt operation.
type Params struct {
	// Command — the terragrunt command (plan/apply/validate). Empty —
	// taken from RunInfo.Command.
	Command string `yaml:"command"`
	// DryRun is a stub: prints the would-be command to Stdout instead of
	// running terragrunt.
	DryRun  bool          `yaml:"dry_run"`
	Timeout time.Duration `yaml:"timeout"`
}

type Op struct {
	command string
	dryRun  bool
	timeout time.Duration
}

func New(command string, dryRun bool, timeout time.Duration) *Op {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Op{command: command, dryRun: dryRun, timeout: timeout}
}

func (o *Op) Name() string { return "terragrunt" }

func (o *Op) RunUnit(ctx context.Context, uc *engine.UnitContext) engine.Result {
	command := o.command
	if command == "" {
		command = string(uc.Run.Run.Command)
	}
	// terragrunt <command> [passthrough args from `oink run -- ...`] <oink flags>.
	// Flag names match terragrunt >= 1.0 (CLI redesign); the version is
	// pinned in the README.
	args := []string{command}
	args = append(args, uc.Run.Run.Args...)
	args = append(args, "--non-interactive", "--working-dir", uc.Unit.Path)

	if o.dryRun {
		return engine.Result{
			Status: engine.StatusOK,
			Stdout: fmt.Sprintf("[dry-run] terragrunt %v (unit %s)\n", args, uc.Unit.Path),
		}
	}

	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "terragrunt", args...)
	if uc.Run.Run.RepoRoot != "" {
		cmd.Dir = uc.Run.Run.RepoRoot
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	res := engine.Result{Status: engine.StatusOK, Stdout: out.String()}
	if runErr != nil {
		res.Status = engine.StatusFailed
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			res.Err = fmt.Errorf("terragrunt timed out after %s: %w", o.timeout, runErr)
		} else {
			res.Err = runErr
		}
	}
	return res
}
