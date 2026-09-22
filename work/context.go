package work

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/m-d-brown/cascade/history"
)

// Context is what a call is handed while it runs: its identity and its
// logging.
//
// It is also the call's [context.Context]: it carries the run's
// cancellation, so work takes one parameter rather than a context beside a
// context:
//
//	func build(ctx *work.Context, goos string) (string, error) {
//	    cmd := exec.CommandContext(ctx, "go", "build", …)   // cancels with the run
//	    …
//	}
//
// Pass it anywhere a context is wanted: `q` in the live display, ^C, a
// critical failure elsewhere in the run all arrive through it.
//
// [Context.Path], [Context.Name], [Context.LogPath] and [Context.Started]
// report the call's identity; they are set by the runner and never change.
// Every logging method is scoped to the call, so a call never has to know
// how the run is being displayed. The same call feeds the run's text log
// (its lines tagged with the call path), the event log and its row in the
// live terminal display.
//
// Nothing here routes an effect anywhere; a call is free to touch the
// outside world however an ordinary Go function would. A call that wants
// that touch to be interceptable reaches for
// [github.com/m-d-brown/cascade/world] explicitly, whose DryRun and
// Confirm are what make that possible.
type Context struct {
	path    string
	name    string
	logPath string
	started time.Time
	ctx     context.Context

	runner *Runner

	mu      sync.Mutex
	sink    func(level slog.Level, kind history.EventKind, msg string)
	summary string
}

// Name is [Context.Path]'s last segment: this call's own name, as given to
// [Do] or [Go].
func (c *Context) Name() string { return c.name }

// LogPath is the run's one text log, where this call's lines are written
// among the rest, each tagged with its call path, or "" if the run has
// nowhere to write logs.
func (c *Context) LogPath() string { return c.logPath }

// Started is when the call began running.
func (c *Context) Started() time.Time { return c.started }

// Path is this call's full path: its own name, preceded by the names of
// every call it is nested inside, joined with "/": "release/build-linux".
// It is the key everything downstream of the call uses: the log file, the
// row in the live display, the box [history.Dot] draws, and what the state
// journal remembers this call by.
func (c *Context) Path() string { return c.path }

// Deadline implements [context.Context].
func (c *Context) Deadline() (time.Time, bool) { return c.context().Deadline() }

// Done implements [context.Context]: it closes when the run is canceled, so
// a call can stop what it is doing.
func (c *Context) Done() <-chan struct{} { return c.context().Done() }

// Err implements [context.Context].
func (c *Context) Err() error { return c.context().Err() }

// Value implements [context.Context]. Data flows between calls as ordinary
// Go values (arguments in, a result out), not through context values.
func (c *Context) Value(key any) any { return c.context().Value(key) }

func (c *Context) context() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// Logf writes a durable log line for this call.
func (c *Context) Logf(format string, a ...any) {
	c.emitLine(slog.LevelInfo, history.TaskLog, fmt.Sprintf(format, a...))
}

// Warnf writes a durable warning line for this call.
func (c *Context) Warnf(format string, a ...any) {
	c.emitLine(slog.LevelWarn, history.TaskLog, fmt.Sprintf(format, a...))
}

// Statusf reports transient progress: only the most recent line matters, and
// the live display shows it on the call's row. Status lines are logged too,
// but observers may drop all but the last.
func (c *Context) Statusf(format string, a ...any) {
	c.emitLine(slog.LevelInfo, history.TaskStatus, fmt.Sprintf(format, a...))
}

// Summarize sets the one-line summary the run shows for this call (the
// table row, the live display, `state`), the same way [Context.Statusf]
// sets the transient one, except this is what stands once the call is
// done. The last call before the call returns wins.
//
// A call that never calls this gets a summary for free: its own return
// value, if it is a non-empty string, or "done" otherwise. That is enough
// for most calls, and why fn returning just a value and an error, without a
// summary of its own to carry, is normal:
//
//	func fetchSource(ctx *work.Context, version string) (string, error) {
//	    …
//	    return "/tmp/widget", nil   // shows as "/tmp/widget"
//	}
//
// Call it when the return value alone would not read well on its own:
//
//	ctx.Summarize("packaged %d archives", len(binaries))
//	return "dist/", nil
func (c *Context) Summarize(format string, a ...any) {
	c.mu.Lock()
	c.summary = fmt.Sprintf(format, a...)
	c.mu.Unlock()
}

func (c *Context) getSummary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.summary
}

func (c *Context) emitLine(level slog.Level, kind history.EventKind, msg string) {
	c.mu.Lock()
	sink := c.sink
	c.mu.Unlock()
	if sink != nil {
		sink(level, kind, msg)
	}
}

// LogWriter returns a writer whose lines become log lines for this call.
// Hand it to exec.Cmd.Stdout to stream a subprocess into the call's log.
func (c *Context) LogWriter() io.WriteCloser {
	return c.lineWriter(func(line string) { c.Logf("%s", line) })
}

// StatusWriter returns a writer whose lines become transient status lines,
// so a chatty subprocess updates the call's row without flooding the
// display.
func (c *Context) StatusWriter() io.WriteCloser {
	return c.lineWriter(func(line string) { c.Statusf("%s", line) })
}

func (c *Context) lineWriter(emit func(string)) io.WriteCloser {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if line != "" {
				emit(line)
			}
		}
		_, _ = io.Copy(io.Discard, pr) // draining to unblock the writer, not reading for content
	}()
	return &pipeWriter{PipeWriter: pw, done: done}
}

type pipeWriter struct {
	*io.PipeWriter
	done chan struct{}
	once sync.Once
}

func (w *pipeWriter) Close() error {
	err := w.PipeWriter.Close()
	w.once.Do(func() { <-w.done })
	return err
}

// Monitor starts a background reporter that calls probe every interval and
// publishes the result as a status line, mirroring the "tail the log every
// 60 seconds" pattern. A probe that fails or returns "" is ignored, so a
// broken progress probe never affects the work it reports on. The returned
// function stops the reporter and waits for it to exit.
//
// The probe runs on its own goroutine while the call works, so anything it
// reads from the call must be safe to share: an atomic, a mutex, or a
// units.Watcher over the running command's output. It is handed the
// monitor's own context, which ends when the run does or when stop is
// called.
func (c *Context) Monitor(interval time.Duration, probe func(context.Context) (string, error)) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(c.context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				msg, err := probe(ctx)
				switch {
				case err != nil:
					c.Statusf("progress: unknown (%v)", err)
				case msg != "":
					c.Statusf("%s", msg)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
