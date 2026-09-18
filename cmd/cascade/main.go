// Command cascade runs a graph of shell actions declared in YAML as a
// workflow.
//
// The cascade file names the actions, what each waits for (needs:), and how
// each decides it is already up to date (produces/sources, unless:, every:).
// This binary reads the file and drives the framework underneath — so the
// live tree, the journal, flow.log, dot, flamegraph, --continue and approval
// prompts all come for free. See package github.com/mdbrown/cascade/interpreter
// for the file format and the design.
//
//	cascade run -f build.yaml         # run the graph
//	cascade run --dry-run             # walk it, running nothing
//	cascade plan                      # a dry run, also journaled
//	cascade run --confirm             # ask before every action
//	cascade status                    # where the last (or running) cascade has got to
//	cascade status -w                 # …and keep refreshing
//	cascade graph -f build.yaml       # print the dependency DAG as Graphviz
//	cascade dot | dot -Tsvg -o r.svg  # draw the run that just happened
//
// Install it with `go install github.com/mdbrown/cascade/cmd/cascade@latest`,
// or run it in place with `go run ./cmd/cascade …`. examples/cascade has a
// self-contained cascade to try it on.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdbrown/cascade/cli"
	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/interpreter"
	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
	"github.com/spf13/pflag"
)

const name = "cascade"

var (
	file    = "cascade.yaml"
	dryRun  bool
	confirm bool
)

func main() {
	// `cascade graph` and `cascade status` are not workflow runs, and cli.App
	// has no hook for an extra subcommand, so they are handled before the
	// framework's command tree sees the arguments.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check":
			os.Exit(checkCmd(os.Args[2:]))
		case "graph":
			os.Exit(graphCmd(os.Args[2:]))
		case "status":
			os.Exit(statusCmd(os.Args[2:]))
		}
	}

	cli.Main(cli.App{
		Name:    name,
		Short:   "run a YAML-declared graph of shell actions",
		Version: "0.1.0",
		Long: `Run a cascade: a YAML file naming shell actions, what each waits for,
and how each decides it is already up to date.

  cascade check               validate the file and report anything suspect
  cascade run                 run the graph
  cascade run --dry-run       walk it, running nothing
  cascade plan                a dry run that is also journaled (see below)
  cascade status [-w]         where the last (or running) cascade has got to
  cascade graph -f r.yaml     print the dependency DAG as Graphviz
  cascade dot | dot -Tsvg     draw the run that just happened

--dry-run routes every action through a world that changes nothing. plan
(and -n / --pretend) marks the journal so the run is never resumed from;
because every action here is an external command, this runner also gives a
plan the dry-run world, so "plan" is always safe to type.`,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVarP(&file, "file", "f", file, "path to the cascade file")
			fs.BoolVar(&dryRun, "dry-run", false, "route every action through a world that changes nothing")
			fs.BoolVar(&confirm, "confirm", false, "ask before every action (and every freshness check)")
		},
		Describe: func() string { return file },
		Flow: func(ctx *work.Context) (string, error) {
			r, err := interpreter.Load(file)
			if err != nil {
				return "", work.Fatal(err)
			}
			// plan / -n / --pretend only mark the journal; the world stays
			// whatever we build here. Since every action is an external
			// command, a plan that ran them for real would be a trap, so a
			// pretend run gets the dry-run world too — the pattern an app
			// opts into when it wants "plan" to mean "touch nothing".
			rehearse := dryRun || pretendRun()
			w := world.Real()
			if rehearse {
				w = world.DryRun()
			}
			if confirm && !rehearse {
				if p := world.PrompterFromContext(ctx); p != nil {
					w = world.Confirm(w, p)
				}
			}
			return r.Run(ctx, w)
		},
		DefaultStatePath: cli.UserStatePath(name),
		DefaultLogDir:    filepath.Join(os.TempDir(), name+"-logs"),
		// A cascade's actions are all known up front and the summary table
		// already says what happened; there's nothing a browse of the
		// finished tree would tell you that `cascade status`/`state` don't,
		// so return to the shell the moment the run ends instead of holding
		// the terminal for a keypress.
		ExitWhenDone: true,
	})
}

// pretendRun reports whether this invocation is `plan` or carries the
// framework's own --pretend / -n. cli.App gives Flow no view of the parsed
// command, so this reads the raw arguments, the same way any app that wants
// plan to imply a dry run does.
func pretendRun() bool {
	for _, a := range os.Args[1:] {
		if a == "plan" || a == "-n" || a == "--pretend" || a == "--pretend=true" {
			return true
		}
	}
	return false
}

// checkCmd implements `cascade check [-f file] [--strict]`: parse and
// validate the cascade without running anything, and report every warning.
func checkCmd(args []string) int {
	fs := pflag.NewFlagSet("check", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, "usage: cascade check [-f cascade.yaml] [--strict]\n") }
	f := fs.StringP("file", "f", "cascade.yaml", "path to the cascade file")
	strict := fs.Bool("strict", false, "exit non-zero if there are warnings, not only errors")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	p, err := interpreter.Load(*f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	warnings := p.Warnings()
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "! %s\n", w)
	}
	n := len(p.Actions())
	switch {
	case len(warnings) == 0:
		fmt.Printf("✓ %s: %d action%s, nothing looks wrong\n", p.Name(), n, plural(n))
		return 0
	default:
		fmt.Printf("%s: %d action%s, %d warning%s\n", p.Name(), n, plural(n), len(warnings), plural(len(warnings)))
		if *strict {
			return 1
		}
		return 0
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// graphCmd implements `cascade graph [-f file]`.
func graphCmd(args []string) int {
	fs := pflag.NewFlagSet("graph", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, "usage: cascade graph [-f cascade.yaml]\n") }
	f := fs.StringP("file", "f", "cascade.yaml", "path to the cascade file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	r, err := interpreter.Load(*f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Print(graphviz(r))
	return 0
}

// graphviz renders a cascade's needs edges as a Graphviz digraph.
func graphviz(r *interpreter.Plan) string {
	actions := r.Actions()
	var b strings.Builder
	fmt.Fprintf(&b, "digraph %q {\n", r.Name())
	b.WriteString("  rankdir=LR;\n  node [shape=box, style=rounded, fontname=Helvetica];\n")
	for _, n := range r.Order() {
		deps := append([]string(nil), actions[n].Needs...)
		sort.Strings(deps)
		if len(deps) == 0 {
			fmt.Fprintf(&b, "  %q;\n", n)
		}
		for _, d := range deps {
			fmt.Fprintf(&b, "  %q -> %q;\n", d, n)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// statusCmd implements `cascade status [run-id] [-w] [--state path]`: where the
// most recent (or named) run has got to, read from its event log so it is
// current even mid-run.
func statusCmd(args []string) int {
	fs := pflag.NewFlagSet("status", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: cascade status [run-id] [-w] [--state path]\n")
	}
	statePath := fs.String("state", cli.UserStatePath(name), "path to the state journal")
	watch := fs.BoolP("watch", "w", false, "refresh every 2s until the run finishes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	want := "last"
	if fs.NArg() > 0 {
		want = fs.Arg(0)
	}

	logDir, err := resolveLogDir(*statePath, want)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	// In --watch, stop when the run finishes — or, if the run process was
	// killed outright (no "finished" event ever written), when nothing has
	// changed for a while and every started action is done.
	var last string
	stallCount := 0
	for {
		rp, err := interpreter.ReadProgress(logDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if *watch {
			fmt.Print("\033[H\033[2J")
		}
		printProgress(os.Stdout, rp)
		if !*watch || rp.Finished {
			return 0
		}
		if snap := progressSnapshot(rp); snap == last {
			stallCount++
		} else {
			stallCount, last = 0, snap
		}
		if stallCount >= 5 && allDone(rp) {
			fmt.Println("\n(no change for 10s and every action is done — the run may have stopped)")
			return 0
		}
		time.Sleep(2 * time.Second)
	}
}

// progressSnapshot is a change-detector string for a RunProgress.
func progressSnapshot(rp *interpreter.RunProgress) string {
	var b strings.Builder
	for _, a := range rp.Actions {
		fmt.Fprintf(&b, "%s=%s/%s;", a.Name, a.Status, a.Detail)
	}
	return b.String()
}

func allDone(rp *interpreter.RunProgress) bool {
	if len(rp.Actions) == 0 {
		return false
	}
	for _, a := range rp.Actions {
		if !a.Finished {
			return false
		}
	}
	return true
}

// resolveLogDir finds the log directory of the run named by want ("last" or an
// id) from the journal at statePath.
func resolveLogDir(statePath, want string) (string, error) {
	j, err := history.NewFileStore(statePath).Load(context.Background())
	if err != nil {
		return "", err
	}
	var run history.Run
	if want == "last" || want == "" {
		recent := j.Recent(1)
		if len(recent) == 0 {
			return "", errors.New("no runs recorded yet")
		}
		run = recent[0]
	} else {
		r, ok := j.Run(want)
		if !ok {
			return "", fmt.Errorf("no run %q in the journal", want)
		}
		run = r
	}
	if run.Logs == "" {
		return "", fmt.Errorf("run %s kept no event log", run.ID)
	}
	return run.Logs, nil
}

// printProgress writes the per-action status table.
func printProgress(w *os.File, rp *interpreter.RunProgress) {
	state := "running"
	if rp.Finished {
		state = "finished"
	}
	fmt.Fprintf(w, "run %s · %s", rp.Run, state)
	switch {
	case rp.Started.IsZero():
	case rp.Finished:
		var last time.Time
		for _, a := range rp.Actions {
			if a.Updated.After(last) {
				last = a.Updated
			}
		}
		if last.After(rp.Started) {
			fmt.Fprintf(w, " · took %s", round(last.Sub(rp.Started)))
		}
	default:
		fmt.Fprintf(w, " · started %s ago", round(time.Since(rp.Started)))
	}
	fmt.Fprintln(w)
	if len(rp.Actions) == 0 {
		fmt.Fprintln(w, "  (no actions have started yet)")
		return
	}
	width := 0
	for _, a := range rp.Actions {
		if len(a.Name) > width {
			width = len(a.Name)
		}
	}
	for _, a := range rp.Actions {
		detail := a.Detail
		if !a.Finished && a.Status == history.Running && detail == "" {
			detail = "…"
		}
		fmt.Fprintf(w, "  %s %-*s  %-8s %6s  %s\n",
			a.Status.Symbol(), width, a.Name, a.Status, round(a.Elapsed()), detail)
	}
}

func round(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond)
	case d < time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(time.Second)
	}
}
