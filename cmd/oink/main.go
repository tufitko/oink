// Command oink is the CLI for the oink terragrunt GitOps runner.
//
// Usage mirrors terragrunt: oink-specific flags come first, the terragrunt
// command and its arguments come after `--`:
//
//	oink run --repo-root . --changed-dirs envs/db -- plan -refresh=false
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tufitko/oink"
	"github.com/tufitko/oink/atlantis"
	"github.com/tufitko/oink/diag"
	"github.com/tufitko/oink/engine"
)

// exitCodeError carries a process exit code up to main without using os.Exit
// deep in the call stack (which would skip deferred cleanup).
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

func main() {
	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		os.Exit(ec.code)
	}
	fmt.Fprintln(os.Stderr, "oink:", err)
	os.Exit(2)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "oink",
		Short:         "GitOps runner for terragrunt",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd())
	root.AddCommand(newGenerateAtlantisConfigCmd())
	return root
}

type runFlags struct {
	configPath  string
	changedDirs []string
	repoRoot    string
	runID       string

	logLevel   string
	pprofAddr  string
	cpuProfile string
	memProfile string
	traceFile  string
}

func newRunCmd() *cobra.Command {
	var f runFlags

	cmd := &cobra.Command{
		Use:   "run [flags] -- <terragrunt-command> [terragrunt-args...]",
		Short: "Run a pipeline of operations over the affected terragrunt units",
		Long: "Run executes the oink pipeline (.tgops.yaml). The terragrunt command and\n" +
			"its arguments are given after `--` and passed through to the per-unit\n" +
			"terragrunt invocations, e.g.:\n\n" +
			"  oink run --repo-root . --changed-dirs envs/db -- plan -refresh=false",
		// Args after `--` are the terragrunt command line, not validated here.
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tgArgs, err := terragruntArgs(cmd, args)
			if err != nil {
				return err
			}
			return runPipeline(cmd.Context(), f, tgArgs)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&f.configPath, "config", ".tgops.yaml", "path to the pipeline config")
	fl.StringSliceVar(&f.changedDirs, "changed-dirs", nil, "changed unit directories (repeatable or comma-separated; empty = all units)")
	fl.StringVar(&f.repoRoot, "repo-root", ".", "root of the repository with terragrunt units")
	fl.StringVar(&f.runID, "run-id", "", "run identifier (empty = generate)")

	fl.StringVar(&f.logLevel, "log-level", "info", "operation execution log level on stderr: debug | info | warn | error | off")
	fl.StringVar(&f.pprofAddr, "pprof-addr", "", "start a pprof HTTP server on this address (e.g. localhost:6060)")
	fl.StringVar(&f.cpuProfile, "cpu-profile", "", "write a CPU profile to this file")
	fl.StringVar(&f.memProfile, "mem-profile", "", "write a heap profile to this file on exit")
	fl.StringVar(&f.traceFile, "trace", "", "write the runtime execution trace to this file (inspect with `go tool trace`)")

	return cmd
}

// terragruntArgs extracts the terragrunt command line from the positional
// args, requiring it to appear after `--`.
func terragruntArgs(cmd *cobra.Command, args []string) ([]string, error) {
	dash := cmd.ArgsLenAtDash()
	if dash > 0 {
		return nil, fmt.Errorf("unexpected arguments before --: %v", args[:dash])
	}
	if dash < 0 || len(args) == 0 {
		return nil, fmt.Errorf("missing terragrunt command; usage: oink run [flags] -- <terragrunt-command> [args...]")
	}
	return args, nil
}

func runPipeline(parent context.Context, f runFlags, tgArgs []string) error {
	logger, err := newLogger(f.logLevel)
	if err != nil {
		return err
	}

	session, err := diag.Start(diag.Config{
		PprofAddr:  f.pprofAddr,
		CPUProfile: f.cpuProfile,
		MemProfile: f.memProfile,
		Trace:      f.traceFile,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := session.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "oink: diagnostics:", err)
		}
	}()
	if session.PprofAddr != "" && logger != nil {
		logger.Info("pprof server listening", "addr", "http://"+session.PprofAddr+"/debug/pprof/")
	}

	runID := f.runID
	if runID == "" {
		runID = "local-" + time.Now().UTC().Format("20060102-150405")
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	status, err := oink.Run(ctx, oink.Options{
		Run: engine.RunInfo{
			RunID:       runID,
			Command:     engine.Command(tgArgs[0]),
			Args:        tgArgs[1:],
			ChangedDirs: f.changedDirs,
			RepoRoot:    f.repoRoot,
		},
		ConfigPath: f.configPath,
		Logger:     logger,
	})
	if err != nil {
		return err
	}
	if status == engine.StatusFailed {
		return &exitCodeError{code: 1}
	}
	return nil
}

// atlantisFlags mirrors `terragrunt-atlantis-config generate`. Flags marked
// "compat-only" are accepted for CLI compatibility but not yet implemented.
type atlantisFlags struct {
	autoplan               bool
	automerge              bool
	ignoreParentTerragrunt bool
	ignoreDependencyBlocks bool
	parallel               bool
	createWorkspace        bool
	createProjectName      bool
	preserveWorkflows      bool
	cascadeDependencies    bool
	useProjectMarkers      bool
	executionOrderGroups   bool
	dependsOn              bool
	workflow               string
	applyRequirements      []string
	terraformVersion       string
	silencePRComments      []string
	repoLocks              string
	output                 string
	filter                 []string
	root                   string
	numExecutors           int

	// compat-only (accepted, not implemented):
	createParentProject            bool
	preserveProjects               bool
	projectHclFiles                []string
	createHclProjectChilds         bool
	createHclProjectExternalChilds bool
}

func newGenerateAtlantisConfigCmd() *cobra.Command {
	var f atlantisFlags
	pwd, _ := os.Getwd()

	cmd := &cobra.Command{
		Use:   "generate-atlantis-config [flags]",
		Short: "Generate an atlantis.yaml from the terragrunt repository",
		Long: "Generates an Atlantis config (functional equivalent of\n" +
			"`terragrunt-atlantis-config generate`): one project per terragrunt unit\n" +
			"with autoplan.when_modified derived from each unit's files and its\n" +
			"dependency closure. Honors atlantis_* locals (workflow, skip,\n" +
			"terraform_version, apply_requirements, autoplan, extra_atlantis_dependencies).",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGenerateAtlantis(cmd.Context(), f)
		},
	}

	fl := cmd.Flags()
	fl.BoolVar(&f.autoplan, "autoplan", false, "Enable auto plan. Default is disabled")
	fl.BoolVar(&f.automerge, "automerge", false, "Enable auto merge. Default is disabled")
	fl.BoolVar(&f.ignoreParentTerragrunt, "ignore-parent-terragrunt", true, "Ignore parent terragrunt configs (those without a terraform source)")
	fl.BoolVar(&f.ignoreDependencyBlocks, "ignore-dependency-blocks", false, "When true, dependencies found in `dependency` blocks will be ignored")
	fl.BoolVar(&f.parallel, "parallel", true, "Enables plans and applies to happen in parallel. Default is enabled")
	fl.BoolVar(&f.createWorkspace, "create-workspace", false, "Use different workspace for each project. Default is use default workspace")
	fl.BoolVar(&f.createProjectName, "create-project-name", false, "Add different name for each project. Default is false")
	fl.BoolVar(&f.preserveWorkflows, "preserve-workflows", true, "Preserves workflows from old output files. Default is true")
	fl.BoolVar(&f.cascadeDependencies, "cascade-dependencies", true, "When true, dependencies cascade transitively. Default is true")
	fl.BoolVar(&f.useProjectMarkers, "use-project-markers", false, "Create projects only for units with locals: atlantis_project")
	fl.BoolVar(&f.executionOrderGroups, "execution-order-groups", false, "Computes execution_order_group for projects")
	fl.BoolVar(&f.dependsOn, "depends-on", false, "Computes depends_on for projects. Requires --create-project-name")
	fl.StringVar(&f.workflow, "workflow", "", "Name of the workflow to set on each project. Default is to not set")
	fl.StringSliceVar(&f.applyRequirements, "apply-requirements", nil, "Requirements before `atlantis apply` (approved, mergeable). Can be overridden by locals")
	fl.StringVar(&f.terraformVersion, "terraform-version", "", "Default terraform version for all modules. Can be overridden by locals")
	fl.StringSliceVar(&f.silencePRComments, "silence-pr-comments", nil, "Silence PR comments for the given commands (plan, apply)")
	fl.StringVar(&f.repoLocks, "repo-locks", "", "Repo lock mode for projects: on_plan, on_apply, disabled")
	fl.StringVar(&f.output, "output", "", "Path to write the config to. Default is stdout")
	fl.StringSliceVar(&f.filter, "filter", nil, "Comma-separated paths or globs to scope the config to. Default is all units")
	fl.StringVar(&f.root, "root", pwd, "Path to the root directory of the repo. Default is current dir")
	fl.IntVar(&f.numExecutors, "num-executors", 15, "Number of parallel workers used for generation")

	// Accepted for CLI compatibility with terragrunt-atlantis-config but not
	// yet implemented; see README.
	fl.BoolVar(&f.createParentProject, "create-parent-project", false, "(compat-only, not implemented)")
	fl.BoolVar(&f.preserveProjects, "preserve-projects", false, "(compat-only, not implemented)")
	fl.StringSliceVar(&f.projectHclFiles, "project-hcl-files", nil, "(compat-only, not implemented)")
	fl.BoolVar(&f.createHclProjectChilds, "create-hcl-project-childs", false, "(compat-only, not implemented)")
	fl.BoolVar(&f.createHclProjectExternalChilds, "create-hcl-project-external-childs", true, "(compat-only, not implemented)")

	return cmd
}

func runGenerateAtlantis(ctx context.Context, f atlantisFlags) error {
	preserve := ""
	if f.preserveWorkflows {
		preserve = f.output // terragrunt-atlantis-config preserves from the old output file
	}

	cfg, err := atlantis.Generate(ctx, atlantis.Options{
		RepoRoot:               f.root,
		Autoplan:               f.autoplan,
		AutoMerge:              f.automerge,
		Parallel:               f.parallel,
		IgnoreParentTerragrunt: f.ignoreParentTerragrunt,
		IgnoreDependencyBlocks: f.ignoreDependencyBlocks,
		CascadeDependencies:    f.cascadeDependencies,
		CreateWorkspace:        f.createWorkspace,
		CreateProjectName:      f.createProjectName,
		UseProjectMarkers:      f.useProjectMarkers,
		ExecutionOrderGroups:   f.executionOrderGroups,
		DependsOn:              f.dependsOn,
		DefaultWorkflow:        f.workflow,
		ApplyRequirements:      f.applyRequirements,
		TerraformVersion:       f.terraformVersion,
		SilencePRComments:      f.silencePRComments,
		RepoLocks:              f.repoLocks,
		Filter:                 f.filter,
		PreserveWorkflows:      preserve,
		NumExecutors:           f.numExecutors,
	})
	if err != nil {
		return err
	}

	out, err := cfg.Marshal()
	if err != nil {
		return err
	}

	if f.output == "" {
		_, err := os.Stdout.Write(out)
		return err
	}
	if err := os.WriteFile(f.output, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "oink: wrote %d projects to %s\n", len(cfg.Projects), f.output)
	return nil
}

func newLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level
	switch level {
	case "off":
		return nil, nil
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid log level %q (want debug, info, warn, error or off)", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})), nil
}
