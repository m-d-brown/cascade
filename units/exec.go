// Package units provides building blocks for writing workflow calls:
// running external commands through the run's world, and fetching secrets
// in one call.
package units

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
)

// Cmd describes an external command run by a call.
type Cmd struct {
	// Path is the program to run. Ignored when Shell is set.
	Path string
	Args []string
	// Shell, when set, is run through "sh -c", for pipelines such as
	// "ssh host tar -cf - /etc | rclone rcat dst".
	Shell string
	Dir   string
	Env   []string
	Stdin io.Reader

	// Quiet routes stdout to transient status lines rather than durable log
	// lines, for commands that print a line per file.
	Quiet bool
	// AllowExit lists non-zero exit codes that are not failures. tar exits 1
	// when files change underneath it, which is a warning, not an error.
	AllowExit []int
	// Instead is the output a dry run hands back in place of running
	// the command, so whatever parses the output still has something of
	// the right shape to work with. See [world.DryRun].
	Instead []string
	// Watch, if set, gives the caller a live view of the output while the
	// command runs, for a progress reporter on another goroutine.
	Watch *Watcher
}

// Watcher exposes a running command's output while it runs, so a call can
// report progress without waiting for the command to finish.
type Watcher struct{ cap capture }

// NewWatcher returns a watcher to attach to a [Cmd].
func NewWatcher() *Watcher { return &Watcher{} }

// TailLine returns the last n captured lines as a single line, joined by
// " · ", or "" if there is no output yet, shaped for a [work.Context.Monitor]
// probe. For the lines themselves, use [Watcher.Lines].
func (w *Watcher) TailLine(n int) string {
	lines := w.cap.lines()
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " · "))
}

// Lines returns everything captured so far.
func (w *Watcher) Lines() []string { return w.cap.lines() }

// String renders the command as it would be typed.
func (c Cmd) String() string {
	if c.Shell != "" {
		return "sh -c " + quote(c.Shell)
	}
	parts := append([]string{c.Path}, c.Args...)
	for i, p := range parts {
		if strings.ContainsAny(p, " \t\"'") {
			parts[i] = quote(p)
		}
	}
	return strings.Join(parts, " ")
}

func quote(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\"" }

// ExecResult is what a command produced.
type ExecResult struct {
	ExitCode int
	// Lines holds the last lines of merged stdout and stderr.
	Lines    []string
	Duration time.Duration
	// DryRun reports that the command did not actually run: its world
	// stood in for it.
	DryRun bool
}

// Find returns the first captured line containing substr.
func (r ExecResult) Find(substr string) (string, bool) {
	for _, l := range r.Lines {
		if strings.Contains(l, substr) {
			return strings.TrimSpace(l), true
		}
	}
	return "", false
}

// Tail returns the last n captured lines.
func (r ExecResult) Tail(n int) []string {
	if len(r.Lines) <= n {
		return r.Lines
	}
	return r.Lines[len(r.Lines)-n:]
}

// Output returns the captured lines joined by newlines.
func (r ExecResult) Output() string { return strings.Join(r.Lines, "\n") }

const maxCapturedLines = 500

// Run executes a command as an effect on w, streaming its output into the
// call's log and capturing the tail for parsing.
//
// The calling code is the same either way: in a real world the command
// runs, and in a dry run it does not and [Cmd.Instead] stands in for
// its output. Nothing here has to ask which world it is in.
func Run(ctx *work.Context, w world.World, c Cmd) (ExecResult, error) {
	cap := &capture{}
	if c.Watch != nil {
		cap = &c.Watch.cap
	}
	return world.Perform(ctx, w, world.Effect[ExecResult]{
		What:    "run " + c.String(),
		Do:      func() (ExecResult, error) { return execute(ctx, c, cap) },
		Instead: ExecResult{Lines: c.Instead, DryRun: true},
	})
}

// execute is the real world's half of [Run].
func execute(ctx *work.Context, c Cmd, cap *capture) (ExecResult, error) {
	var cmd *exec.Cmd
	if c.Shell != "" {
		cmd = exec.CommandContext(ctx, "sh", "-c", c.Shell)
	} else {
		if _, err := exec.LookPath(c.Path); err != nil {
			return ExecResult{}, fmt.Errorf("%s: %w", c.Path, err)
		}
		cmd = exec.CommandContext(ctx, c.Path, c.Args...)
	}
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin = c.Stdin
	// When the run is canceled or a Timeout fires, kill the whole process
	// group, not just the shell: a command that backgrounded a child, or a
	// shell that forked rather than exec'd, would otherwise leave that child
	// running, and holding the output pipe, so Wait blocks on it. WaitDelay
	// is the backstop if even that does not finish the I/O.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second

	var sink io.WriteCloser
	if c.Quiet {
		sink = ctx.StatusWriter()
	} else {
		sink = ctx.LogWriter()
	}
	cmd.Stdout = io.MultiWriter(sink, cap)
	cmd.Stderr = cmd.Stdout

	start := time.Now()
	err := cmd.Run()
	_ = sink.Close() // a pipeWriter's Close is always nil; it only ever waits for the drain

	res := ExecResult{Lines: cap.lines(), Duration: time.Since(start)}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		if !allowed(res.ExitCode, c.AllowExit) {
			for _, l := range res.Tail(10) {
				ctx.Warnf("| %s", l)
			}
			return res, fmt.Errorf("%s exited %d", commandName(c), res.ExitCode)
		}
		ctx.Warnf("%s exited %d, treated as success", commandName(c), res.ExitCode)
	default:
		return res, fmt.Errorf("%s: %w", commandName(c), err)
	}
	return res, nil
}

func allowed(code int, allow []int) bool {
	for _, a := range allow {
		if a == code {
			return true
		}
	}
	return false
}

func commandName(c Cmd) string {
	if c.Shell != "" {
		return "shell command"
	}
	return c.Path
}

// capture keeps the tail of a stream.
type capture struct {
	mu   sync.Mutex
	buf  strings.Builder
	seen []string
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Write(p)
	text := c.buf.String()
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		c.push(strings.TrimRight(text[:i], "\r"))
		text = text[i+1:]
	}
	c.buf.Reset()
	c.buf.WriteString(text)
	return len(p), nil
}

// push records a line. The caller must hold c.mu.
func (c *capture) push(line string) {
	c.seen = append(c.seen, line)
	if len(c.seen) > maxCapturedLines {
		c.seen = c.seen[len(c.seen)-maxCapturedLines:]
	}
}

func (c *capture) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rest := strings.TrimSpace(c.buf.String()); rest != "" {
		c.push(rest)
		c.buf.Reset()
	}
	return append([]string(nil), c.seen...)
}
