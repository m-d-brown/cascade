package interpreter

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
)

// inRun runs fn as the body of a one-shot workflow and returns what it
// produced, so a test can exercise something that needs a real *work.Context.
func inRun[T any](t *testing.T, opts work.Options, fn func(ctx *work.Context) (T, error)) (T, error) {
	t.Helper()
	var got T
	var gotErr error
	if _, err := work.NewRunner(func(ctx *work.Context) (string, error) {
		got, gotErr = fn(ctx)
		return "", nil
	}, opts).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return got, gotErr
}

// runCascade runs r to completion and returns the run result.
func runCascade(t *testing.T, r *Plan, w world.World, opts work.Options) *history.Result {
	t.Helper()
	res, err := work.NewRunner(func(ctx *work.Context) (string, error) {
		return r.Run(ctx, w)
	}, opts).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func mustParse(t *testing.T, yaml string) *Plan {
	t.Helper()
	r, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

// warnCollector is an [history.Observer] that keeps every warning line a run
// emits, so a test can assert cascade told the user about a bad condition.
type warnCollector struct {
	mu    sync.Mutex
	lines []string
}

func (c *warnCollector) Handle(e history.Event) {
	if (e.Kind == history.TaskLog || e.Kind == history.RunLog) && e.Level >= slog.LevelWarn {
		c.mu.Lock()
		c.lines = append(c.lines, e.Path+": "+e.Message)
		c.mu.Unlock()
	}
}

func (c *warnCollector) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

// runWithWarnings runs r and returns every warning line it logged.
func runWithWarnings(t *testing.T, r *Plan, w world.World) []string {
	t.Helper()
	wc := &warnCollector{}
	if _, err := work.NewRunner(func(ctx *work.Context) (string, error) {
		return r.Run(ctx, w)
	}, work.Options{Observer: wc}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return wc.all()
}

func hasWarning(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
