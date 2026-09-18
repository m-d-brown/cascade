// Package cli turns a workflow into a command-line program: run, plan, dot,
// state, runs, logs and flamegraph, all sharing one runner.
//
// An application supplies a name and its workflow function; the framework
// supplies everything else.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/ui"
	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// App describes a workflow application.
type App struct {
	// Name is the program name used in help output.
	Name string
	// Short and Long describe the program.
	Short string
	Long  string
	// Version is reported by `--version`.
	Version string
	// Flags registers application-specific flags, bound to the app's own
	// variables. It is called once, before the workflow runs.
	Flags func(fs *pflag.FlagSet)
	// Flow is the workflow: the whole of what a run does. It is an
	// ordinary function that calls other functions through [work.Do] and
	// [work.Go] as it goes — there is nothing to collect or assemble
	// beforehand, which is why this is the entire shape of App.
	//
	// It takes exactly the shape [work.Do] itself takes, since the run's
	// root is not otherwise different from any other named call. A
	// workflow whose own top-level function already returns what is worth
	// showing — a release's own URL — can be named directly, with nothing
	// wrapped around it: Flow: release. One that only calls through to a
	// single named call needs no more than returning that call's own
	// result:
	//
	//	Flow: func(ctx *work.Context) (string, error) {
	//	    return work.Do(ctx, "report", report)
	//	}
	Flow func(ctx *work.Context) (string, error)
	// Describe returns a one-line description of this run's own input —
	// the version being released, whatever makes this run worth telling
	// apart from another — for the run list and `logs`. Called once per
	// run, after Flags parses, so it can read whatever your own flags read.
	// Optional: a workflow with nothing that varies between runs can leave
	// it nil, and the run list simply shows nothing extra.
	Describe func() string
	// DefaultStatePath is where the run journal lives (default
	// "./<name>-state.json").
	DefaultStatePath string
	// DefaultLogDir is the base directory for per-run log directories
	// (default "./<name>-logs").
	DefaultLogDir string
	// ExitWhenDone skips holding the finished live display open for
	// browsing (↑/↓, enter, q to quit) and returns to the shell the moment
	// the run itself ends, on a terminal same as anywhere else. The default,
	// false, is right for a hand-authored workflow, where stepping through
	// what a run just did is worth keeping the terminal for; a pipeline
	// runner that already reports itself as a table — cascade sets this —
	// wants control back immediately instead.
	ExitWhenDone bool
}

type options struct {
	jobs        int
	pretend     bool
	cont        string
	stopOnError bool
	statePath   string
	logDir      string
	plain       bool
	verbose     bool
	noState     bool
}

func (o *options) register(fs *pflag.FlagSet, app App) {
	fs.IntVarP(&o.jobs, "jobs", "j", 0, "maximum calls started with Go running at once (0 = unbounded)")
	fs.BoolVarP(&o.pretend, "pretend", "n", false, "mark this run as one that changed nothing: journaled for dot/flamegraph, never resumed from")
	fs.StringVar(&o.cont, "continue", "", "finish an earlier run: calls it already made are not made again (id, or \"last\")")
	fs.BoolVar(&o.stopOnError, "stop-on-error", false, "abort the whole run on the first failure")
	fs.StringVar(&o.statePath, "state", firstNonEmpty(app.DefaultStatePath, app.Name+"-state.json"), "path to the state journal")
	fs.BoolVar(&o.noState, "no-state", false, "neither read nor record state")
	fs.StringVar(&o.logDir, "log-dir", firstNonEmpty(app.DefaultLogDir, app.Name+"-logs"), "base directory for per-run logs")
	fs.BoolVar(&o.plain, "plain", false, "plain line output instead of the live display")
	fs.BoolVarP(&o.verbose, "verbose", "v", false, "include transient progress lines in plain output")
}

// Main runs the app and exits with its status code.
func Main(app App) {
	os.Exit(Execute(app))
}

// Execute runs the app and returns the process exit code.
func Execute(app App) int {
	if app.Name == "" {
		app.Name = "flow"
	}
	var opt options
	root := &cobra.Command{
		Use:           app.Name,
		Short:         firstNonEmpty(app.Short, "a cascade workflow"),
		Long:          app.Long,
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	opt.register(root.PersistentFlags(), app)
	if app.Flags != nil {
		app.Flags(root.PersistentFlags())
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "run the workflow",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflow(cmd, &opt, app)
		},
	}

	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "run the workflow, marking the run so it is never resumed from",
		Long: "Run the workflow with --pretend: mark the run so it is journaled — dot and\n" +
			"flamegraph can still show its trace afterward — but never eligible to be\n" +
			"resumed from. plan is `run --pretend`, kept as its own name because a\n" +
			"disposable run is a question worth asking on its own.\n\n" +
			"This does not by itself stop any effect from happening for real: whether an\n" +
			"effect actually happens is a property of the world a call's own code chose to\n" +
			"route it through (see the world package), not of this flag. An app that wants\n" +
			"--pretend/plan to also mean \"touch nothing\" builds a pretend world of its own\n" +
			"and wires it in.",
		RunE: func(cmd *cobra.Command, args []string) error {
			planOpt := opt
			planOpt.pretend = true
			planOpt.cont = "" // continuing a plan makes no sense
			return runWorkflow(cmd, &planOpt, app)
		},
	}

	var dotOpts struct {
		cluster bool
		lr      bool
	}
	dotCmd := &cobra.Command{
		Use:   "dot [run-id]",
		Short: "render a run's trace as Graphviz DOT (the most recent run if none is named)",
		Long: "Render a run's trace as Graphviz DOT: one box per call it made, nested\n" +
			"under the call that made it.\n\n  " + app.Name + " dot | dot -Tpng -o flow.png",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := "last"
			if len(args) > 0 {
				want = args[0]
			}
			return printDot(cmd, &opt, app, want, dotOpts.cluster, dotOpts.lr)
		},
	}
	dotCmd.Flags().BoolVar(&dotOpts.cluster, "cluster", true, "group calls by their first tag")
	dotCmd.Flags().BoolVar(&dotOpts.lr, "left-to-right", false, "lay the graph out horizontally")

	stateCmd := &cobra.Command{
		Use:   "state",
		Short: "show the recorded state journal",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printState(cmd, &opt)
		},
	}

	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "list previous runs, and which of them can be continued",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printRuns(cmd, &opt)
		},
	}

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "show the last runs, then ask what to do",
		RunE: func(cmd *cobra.Command, args []string) error {
			return start(cmd, app, &opt)
		},
	}

	logsCmd := &cobra.Command{
		Use:   "logs [run-id]",
		Short: "print a run's combined log (the most recent run if none is named)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := "last"
			if len(args) > 0 {
				want = args[0]
			}
			return printLogs(cmd, &opt, want)
		},
	}

	flamegraphCmd := &cobra.Command{
		Use:   "flamegraph [run-id]",
		Short: "print a run's timeline as Chrome Trace Event Format JSON (chrome://tracing, ui.perfetto.dev)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := "last"
			if len(args) > 0 {
				want = args[0]
			}
			return printFlamegraph(cmd, &opt, want)
		},
	}

	root.AddCommand(runCmd, planCmd, dotCmd, stateCmd, runsCmd, startCmd, logsCmd, flamegraphCmd)

	// A bare invocation on a terminal is the front door: what the last runs
	// did, and the choice between starting a new one and finishing one that
	// stopped early. Anywhere else — cron, CI, a pipe — there is nobody to
	// ask, so it runs.
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if !interactive(cmd) {
			return runCmd.RunE(cmd, args)
		}
		return startCmd.RunE(cmd, args)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if code, ok := exitCode(root); ok {
		return code
	}
	return 0
}

// exitCodeKey stores the run's exit code on the command so Execute can
// return it without panicking through cobra.
type exitCodeKey struct{}

func setExitCode(cmd *cobra.Command, code int) {
	cmd.Root().SetContext(context.WithValue(cmd.Root().Context(), exitCodeKey{}, code))
}

func exitCode(root *cobra.Command) (int, bool) {
	if root.Context() == nil {
		return 0, false
	}
	code, ok := root.Context().Value(exitCodeKey{}).(int)
	return code, ok
}

// runWorkflow is what `run` and `plan` share: build a runner from the app's
// workflow and today's flags, run it, and print the result.
func runWorkflow(cmd *cobra.Command, opt *options, app App) error {
	if app.Flow == nil {
		return errors.New("app declares no Flow: there is nothing to run")
	}

	logs, err := history.NewLogDir(opt.logDir)
	if err != nil {
		return err
	}
	defer func() { _ = logs.Close() }()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	stdout, _ := cmd.OutOrStdout().(*os.File)
	interactiveDisplay := !opt.plain && ui.IsTerminal(stdout)

	var observer history.Observer
	var live *ui.Live
	// The prompter is the run's approval UI: the live tree's inline modal in
	// a terminal, the plain line-by-line prompt otherwise. It goes into the
	// context so a workflow that wants a human in the loop can pull it out
	// with world.PrompterFromContext and hand it to world.Confirm, instead of
	// building one of its own that writes over the live display. cli still
	// never builds or wires up a World itself — see docs/design.md.
	var prompter world.Prompter
	if interactiveDisplay {
		live = ui.NewLive(stdout, os.Stdin, cancel, app.ExitWhenDone)
		live.Start()
		observer = live
		prompter = live
	} else {
		plain := ui.NewPlain(cmd.OutOrStdout(), cmd.InOrStdin(), opt.verbose)
		observer = plain
		prompter = plain
	}
	ctx = world.WithPrompter(ctx, prompter)

	if opt.pretend {
		fmt.Fprintln(cmd.ErrOrStderr(), "marking this run as pretend: it will be journaled, but never eligible to be resumed from")
	}
	// A pretend run is still journaled — marked as one, so dot and
	// flamegraph can show its trace — but [history.Run.Pretend] keeps it
	// permanently ineligible to stand in for real work. Whether any effect
	// the workflow describes actually happens is a property of the world
	// its own code chose to route it through (see the world package), not of this
	// flag — the framework has no way to reach into an app's own calls to
	// decide that for it.
	var store history.Store
	if !opt.noState {
		store = history.NewFileStore(opt.statePath)
	}

	// Everything a run does is written down the same way, whoever is
	// watching: one JSON object per event, beside the logs, in the order it
	// happened.
	observer = history.Observers(observer, history.LogEvents(logs.Events()))

	carryOn := opt.cont
	if carryOn != "" && store == nil {
		return fmt.Errorf("--continue needs the state journal, and this run has none (--no-state leaves it alone)")
	}
	if carryOn != "" {
		if carryOn, err = resolveRun(cmd, store, carryOn); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "continuing run %s: what it already did is not done again\n", carryOn)
	}

	var input string
	if app.Describe != nil {
		input = app.Describe()
	}
	runner := work.NewRunner(app.Flow, work.Options{
		Name:        app.Name,
		Concurrency: opt.jobs,
		Pretend:     opt.pretend,
		StopOnError: opt.stopOnError,
		Store:       store,
		Input:       input,
		Continue:    carryOn,
		Observer:    observer,
		Logs:        logs,
	})

	fmt.Fprintf(cmd.ErrOrStderr(), "run %s · logs: %s\n", logs.Run(), logs.Dir)
	res, err := runner.Run(ctx)
	if live != nil {
		// A run that reached its own end leaves the tree on screen to step
		// through; Wait returns when the user dismisses it (at once if there
		// is no terminal holding it open). A run cut short by a signal skips
		// straight to teardown.
		if ctx.Err() == nil {
			live.Wait()
		}
		live.Stop()
	}
	if err != nil {
		return err
	}
	ui.Summary(cmd.OutOrStdout(), res)
	setExitCode(cmd, res.ExitCode())
	return nil
}

func printDot(cmd *cobra.Command, opt *options, app App, want string, cluster, lr bool) error {
	store := history.NewFileStore(opt.statePath)
	id, err := resolveRun(cmd, store, want)
	if err != nil {
		return err
	}
	journal, err := store.Load(cmd.Context())
	if err != nil {
		return err
	}
	theRun, ok := journal.Run(id)
	if !ok {
		return fmt.Errorf("no run %q", id)
	}
	fmt.Fprintln(cmd.OutOrStdout(), history.Dot(theRun, history.DotOptions{Title: app.Name, Cluster: cluster, LeftToRight: lr}))
	return nil
}

func printState(cmd *cobra.Command, opt *options) error {
	journal, err := history.NewFileStore(opt.statePath).Load(cmd.Context())
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	paths := journal.Paths()
	if len(paths) == 0 {
		fmt.Fprintf(out, "no recorded state at %s\n", opt.statePath)
		return nil
	}
	fmt.Fprintf(out, "state: %s (updated %s)\n\n", opt.statePath, journal.Updated.Format(time.RFC3339))
	for _, path := range paths {
		rec, runID, ok := journal.MostRecent(path)
		if !ok {
			continue
		}
		fmt.Fprintf(out, "%-28s %-10s %s  %-20s %s\n", path, rec.Status,
			rec.Finished.Format("2006-01-02 15:04"), runID, rec.Summary)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// UserStatePath returns a per-user state path for name, under
// $XDG_STATE_HOME or ~/.local/state — a value for [App.DefaultStatePath].
func UserStatePath(name string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return name + "-state.json"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "cascade", name+".json")
}
