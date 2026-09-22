package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/m-d-brown/cascade/history"
	"github.com/m-d-brown/cascade/ui"
	"github.com/spf13/cobra"
)

// start is the front door: what this workflow did last time, and the
// question that follows from it: start a new run, or finish one that never
// got to the end?
//
// It is what a bare invocation does on a terminal. Everything it offers is a
// command you can also type (`run`, `run --continue <id>`, `plan`), so the
// screen is a shortcut and never the only way in.
func start(cmd *cobra.Command, app App, opt *options) error {
	journal, err := history.NewFileStore(opt.statePath).Load(cmd.Context())
	if err != nil {
		return err
	}
	runs := continuable(journal)

	out := cmd.OutOrStdout()
	in := bufio.NewScanner(cmd.InOrStdin())
	for {
		printSummary(out, app, opt)
		printRecent(out, journal)
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  [enter] start a new run")
		switch {
		case len(runs) == 1:
			fmt.Fprintf(out, "   [1] finish run 1")
		case len(runs) > 1:
			fmt.Fprintf(out, "   [1-%d] finish an earlier run", len(runs))
		}
		fmt.Fprintf(out, "   [p] plan   [s] state   [l] logs   [f] flamegraph   [q] quit\n> ")

		if !in.Scan() {
			fmt.Fprintln(out)
			return nil
		}
		switch choice := strings.ToLower(strings.TrimSpace(in.Text())); choice {
		case "", "n", "r", "run", "y", "yes":
			return runWorkflow(cmd, opt, app)
		case "q", "quit", "exit":
			return nil
		case "p", "plan":
			planOpt := *opt
			planOpt.plan, planOpt.cont = true, ""
			if err := runWorkflow(cmd, &planOpt, app); err != nil {
				fmt.Fprintln(out, err)
			}
		case "s", "state":
			if err := printState(cmd, opt); err != nil {
				fmt.Fprintln(out, err)
			}
		case "l", "logs":
			want, ok := askRunID(in, out)
			if !ok {
				return nil
			}
			if err := printLogs(cmd, opt, want); err != nil {
				fmt.Fprintln(out, err)
			}
		case "f", "flamegraph":
			want, ok := askRunID(in, out)
			if !ok {
				return nil
			}
			if err := printFlamegraph(cmd, opt, want); err != nil {
				fmt.Fprintln(out, err)
			}
		default:
			n, err := strconv.Atoi(choice)
			if err != nil || n < 1 || n > len(runs) {
				fmt.Fprintf(out, "\n%q is not one of the choices.\n\n", choice)
				continue
			}
			contOpt := *opt
			contOpt.cont = runs[n-1].ID
			return runWorkflow(cmd, &contOpt, app)
		}
		fmt.Fprintln(out)
	}
}

// askRunID prompts for a run id, defaulting to the most recent. ok is false
// on EOF, meaning the caller should stop asking anything else.
func askRunID(in *bufio.Scanner, out io.Writer) (id string, ok bool) {
	fmt.Fprint(out, "  run id (blank = last): ")
	if !in.Scan() {
		fmt.Fprintln(out)
		return "", false
	}
	if want := strings.TrimSpace(in.Text()); want != "" {
		return want, true
	}
	return "last", true
}

// interactive reports whether there is somebody there to ask.
func interactive(cmd *cobra.Command) bool {
	stdout, _ := cmd.OutOrStdout().(*os.File)
	stdin, _ := cmd.InOrStdin().(*os.File)
	return ui.IsTerminal(stdout) && ui.IsTerminal(stdin)
}

// printSummary says what this workflow is and where its state lives. There
// is no shape to summarize beyond that (no wave count, no step list), since
// what a workflow does is discovered by running it, not read off a graph.
func printSummary(w io.Writer, app App, opt *options) {
	fmt.Fprintf(w, "\n%s", app.Name)
	if app.Short != "" {
		fmt.Fprintf(w, " — %s", app.Short)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "\n  state: %s   logs: %s\n", opt.statePath, opt.logDir)
}

// printRecent lists what previous runs did, and which of them could be
// finished rather than repeated.
func printRecent(w io.Writer, journal *history.Journal) {
	recent := journal.Recent(5)
	if len(recent) == 0 {
		fmt.Fprintf(w, "\n  no previous runs\n")
		return
	}
	runs := continuable(journal)
	number := map[string]int{}
	for i, run := range runs {
		number[run.ID] = i + 1
	}
	fmt.Fprintf(w, "\nprevious runs\n")
	for _, run := range recent {
		mark := "  "
		if n, ok := number[run.ID]; ok {
			mark = fmt.Sprintf("%d.", n)
		}
		planLabel := ""
		if run.Plan {
			planLabel = " (plan)"
		}
		fmt.Fprintf(w, "  %-3s %s %s %-12s %-24.24s %-34s %s%s\n",
			mark, run.Started.Format("2006-01-02 15:04"), run.Status.Symbol(),
			runWord(run), run.Input, counts(run), duration(run), planLabel)
	}
}

// continuable returns the recent runs worth continuing, newest first: ones
// that never reached an end of their own, or that ended in failure. A run
// that finished cleanly, and a run made only to see the shape of things,
// have nothing left to continue.
func continuable(journal *history.Journal) []history.Run {
	var out []history.Run
	for _, r := range journal.Recent(5) {
		if r.Plan {
			continue
		}
		if !r.Done() || r.Status == history.Failed {
			out = append(out, r)
		}
	}
	return out
}

func runWord(r history.Run) string {
	if !r.Done() {
		return "interrupted"
	}
	return r.Status.String()
}

func counts(r history.Run) string {
	c := r.Counts()
	var parts []string
	for _, s := range []history.Status{history.Succeeded, history.Resumed, history.Skipped, history.Failed, history.Canceled} {
		if c[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c[s], s))
		}
	}
	if len(parts) == 0 {
		return "nothing recorded"
	}
	return strings.Join(parts, ", ")
}

func duration(r history.Run) string {
	if !r.Done() || r.Finished.IsZero() {
		return ""
	}
	return round(r.Finished.Sub(r.Started)).String()
}

func round(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(10 * time.Millisecond)
}

// printRuns is the `runs` command: the history, and how to carry one on.
func printRuns(cmd *cobra.Command, opt *options) error {
	journal, err := history.NewFileStore(opt.statePath).Load(cmd.Context())
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	recent := journal.Recent(20)
	if len(recent) == 0 {
		fmt.Fprintf(out, "no runs recorded at %s\n", opt.statePath)
		return nil
	}
	for _, run := range recent {
		planLabel := ""
		if run.Plan {
			planLabel = " (plan)"
		}
		fmt.Fprintf(out, "%-20s %s %-12s %-24.24s %s%s\n", run.ID, run.Status.Symbol(),
			runWord(run), run.Input, counts(run), planLabel)
		if run.Logs != "" {
			fmt.Fprintf(out, "%-20s logs: %s (or: %s logs %s)\n", "", run.Logs, cmd.Root().Name(), run.ID)
		}
		if !run.Plan && !run.Done() {
			fmt.Fprintf(out, "%-20s finish it: %s run --continue %s\n", "", cmd.Root().Name(), run.ID)
		}
	}
	return nil
}

// printLogs prints a run's combined log: every line, in the order it
// happened, the same content the live display was built from. So
// `logs <id>` and the [l] choice in start need nothing but a run id.
func printLogs(cmd *cobra.Command, opt *options, want string) error {
	store := history.NewFileStore(opt.statePath)
	id, err := resolveRun(cmd, store, want)
	if err != nil {
		return err
	}
	journal, err := store.Load(cmd.Context())
	if err != nil {
		return err
	}
	run, ok := journal.Run(id)
	if !ok {
		return fmt.Errorf("no run %q", id)
	}
	if run.Logs == "" {
		return fmt.Errorf("run %q has no recorded log directory", id)
	}
	data, err := os.ReadFile(filepath.Join(run.Logs, "flow.log"))
	if err != nil {
		return fmt.Errorf("reading the log for run %q: %w", id, err)
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}

// printFlamegraph prints a run's timeline as Chrome Trace Event Format
// JSON. Drag the output into chrome://tracing or ui.perfetto.dev to see
// which calls overlapped and which ran back to back.
func printFlamegraph(cmd *cobra.Command, opt *options, want string) error {
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
	fmt.Fprintln(cmd.OutOrStdout(), history.Flamegraph(theRun))
	return nil
}

// resolveRun turns what the user asked for into a run id, so that "last"
// means the most recent run.
func resolveRun(cmd *cobra.Command, store history.Store, want string) (string, error) {
	journal, err := store.Load(cmd.Context())
	if err != nil {
		return "", err
	}
	if want != "last" {
		if _, ok := journal.Run(want); !ok {
			var have []string
			for _, run := range journal.Recent(5) {
				have = append(have, run.ID)
			}
			if len(have) == 0 {
				return "", fmt.Errorf("no run %q: nothing has been recorded yet", want)
			}
			return "", fmt.Errorf("no run %q (recent runs: %s)", want, strings.Join(have, ", "))
		}
		return want, nil
	}
	recent := journal.Recent(1)
	if len(recent) == 0 {
		return "", fmt.Errorf("there is no last run")
	}
	return recent[0].ID, nil
}
