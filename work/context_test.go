package work

import (
	"context"
	"testing"
	"time"

	"github.com/mdbrown/cascade/history"
)

// funcObserver adapts a function to history.Observer, the way newObserver does
// inside the run package itself — this file just needs its own copy to
// build one from a closure.
type funcObserver func(history.Event)

func (f funcObserver) Handle(e history.Event) { f(e) }

func TestContextLogWriterAndStatusWriterEmitLines(t *testing.T) {
	var logs, statuses []string
	runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "work", func(ctx *Context) (bool, error) {
			lw := ctx.LogWriter()
			_, _ = lw.Write([]byte("line one\nline two\n"))
			_ = lw.Close()

			sw := ctx.StatusWriter()
			_, _ = sw.Write([]byte("42%\n"))
			_ = sw.Close()
			return true, nil
		})
		return err
	}, Options{Observer: funcObserver(func(e history.Event) {
		switch e.Kind {
		case history.TaskLog:
			logs = append(logs, e.Message)
		case history.TaskStatus:
			statuses = append(statuses, e.Message)
		}
	})})

	if len(logs) != 2 || logs[0] != "line one" || logs[1] != "line two" {
		t.Fatalf("logs = %v", logs)
	}
	if len(statuses) != 1 || statuses[0] != "42%" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestContextMonitorPublishesUntilStopped(t *testing.T) {
	var statuses []string
	runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "work", func(ctx *Context) (bool, error) {
			stop := ctx.Monitor(10*time.Millisecond, func(context.Context) (string, error) {
				return "tick", nil
			})
			time.Sleep(35 * time.Millisecond)
			stop()
			return true, nil
		})
		return err
	}, Options{Observer: funcObserver(func(e history.Event) {
		if e.Kind == history.TaskStatus {
			statuses = append(statuses, e.Message)
		}
	})})
	if len(statuses) == 0 {
		t.Fatal("Monitor never published a status line")
	}
}

func TestContextDeadlineAndValueDelegateToTheRunContext(t *testing.T) {
	runFor(t, func(ctx *Context) error {
		if ctx.Value("anything") != nil {
			t.Fatal("Value should be nil for an unset key")
		}
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("Deadline should report none by default")
		}
		return nil
	}, Options{})
}

func TestContextOnNilPointerIsSafe(t *testing.T) {
	var c *Context
	if _, ok := c.Deadline(); ok {
		t.Fatal("nil Context.Deadline should report no deadline")
	}
	if c.Err() != nil {
		t.Fatal("nil Context.Err should be nil")
	}
}
