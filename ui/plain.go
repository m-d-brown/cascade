// Package ui renders a run in a terminal: a live tree while it runs, a
// summary when it finishes, and a plain line-per-event fallback for logs and
// non-interactive output.
package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/world"
)

// Plain is a [history.Observer] and [world.Prompter] that writes one line per
// event. It is the fallback when stdout is not a terminal — cron runs, CI,
// piped output.
type Plain struct {
	w           io.Writer
	in          io.Reader
	verbose     bool
	approvedAll bool

	mu    sync.Mutex
	start time.Time
}

// NewPlain returns a plain observer writing to out and reading approval
// answers from in (nil means [os.Stdin]). verbose includes transient
// progress lines. It parallels [NewLive].
func NewPlain(out io.Writer, in io.Reader, verbose bool) *Plain {
	if in == nil {
		in = os.Stdin
	}
	return &Plain{w: out, in: in, verbose: verbose}
}

// Handle implements [history.Observer].
func (p *Plain) Handle(e history.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case history.RunStarted:
		p.start = e.Time
		fmt.Fprintf(p.w, "%s run %s started\n", stamp(e.Time), e.Run)
	case history.RunLog:
		fmt.Fprintf(p.w, "%s %-24s %s\n", stamp(e.Time), "run", e.Message)
	case history.TaskStarted:
		fmt.Fprintf(p.w, "%s %-24s %s\n", stamp(e.Time), e.Path, startMessage(e.Status))
	case history.TaskLog:
		level := ""
		if e.Level >= slog.LevelWarn {
			level = e.Level.String() + " "
		}
		fmt.Fprintf(p.w, "%s %-24s %s%s\n", stamp(e.Time), e.Path, level, e.Message)
	case history.TaskStatus:
		if p.verbose {
			fmt.Fprintf(p.w, "%s %-24s … %s\n", stamp(e.Time), e.Path, e.Message)
		}
	case history.TaskFinished:
		fmt.Fprintf(p.w, "%s %-24s %s %s (%s)\n", stamp(e.Time), e.Path,
			e.Status.Symbol(), e.Result.Summary, round(e.Result.Duration))
	case history.RunFinished:
		fmt.Fprintf(p.w, "%s run finished in %s\n", stamp(e.Time), round(e.Outcome.Duration))
	}
}

// PromptApproval implements [world.Prompter].
func (p *Plain) PromptApproval(ctx context.Context, req world.Request) (world.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.approvedAll {
		return world.ApproveAll, nil
	}

	fmt.Fprintf(p.w, "\n[APPROVE] %s\n", req.Task)
	fmt.Fprintf(p.w, "          Effect:\n")
	for _, line := range strings.Split(req.Target, "\n") {
		fmt.Fprintf(p.w, "            %s\n", line)
	}
	fmt.Fprintf(p.w, "          Execute? [y/n/p/d/a/q/?] (default: y): ")

	scanner := bufio.NewScanner(p.in)
	for {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return world.AbortRun, err
			}
			return world.ApproveOnce, nil
		}
		choice := strings.ToLower(strings.TrimSpace(scanner.Text()))
		switch choice {
		case "", "y", "yes":
			return world.ApproveOnce, nil
		case "n", "no":
			return world.SkipOnce, nil
		case "p", "perm", "permanent":
			return world.ApprovePermanent, nil
		case "d", "dry-run":
			return world.DryRunOnce, nil
		case "a", "all", "always":
			p.approvedAll = true
			return world.ApproveAll, nil
		case "q", "quit", "abort":
			return world.AbortRun, fmt.Errorf("action aborted by user: %s", req.Target)
		case "?", "help":
			fmt.Fprintf(p.w, "          Options: [y]es: do it once | [n]o: skip it once | [p]ermanent: approve and save to policy | [d]ry-run: log it and do nothing | [a]ll: approve all remaining | [q]uit: abort\n")
			fmt.Fprintf(p.w, "          Execute? [y/n/p/d/a/q/?] (default: y): ")
		default:
			fmt.Fprintf(p.w, "          Unrecognized choice %q. Enter [y/n/p/d/a/q/?]: ", choice)
		}
	}
}

func stamp(t time.Time) string { return t.Format("15:04:05") }

func round(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(10 * time.Millisecond)
}
