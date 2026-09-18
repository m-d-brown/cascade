package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdbrown/cascade/history"
)

// errOnly adapts a plain error-returning workflow to the (string, error)
// shape NewRunner wants, for tests that only care about the error.
// NewRunner itself takes the same shape [Do] does, since the root is not
// otherwise different from any other call.
func errOnly(fn func(ctx *Context) error) func(ctx *Context) (string, error) {
	return func(ctx *Context) (string, error) { return "", fn(ctx) }
}

func runFor(t *testing.T, root func(ctx *Context) error, opts Options) *history.Result {
	t.Helper()
	res, err := NewRunner(errOnly(root), opts).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRunWritesOneTextLogTaggedByCall(t *testing.T) {
	ld, err := history.NewLogDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ld.Close() }()

	var runLog string
	runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "build", func(ctx *Context) (bool, error) {
			ctx.Logf("compiling")
			return true, nil
		})
		return err
	}, Options{Logs: ld, Observer: funcObserver(func(e history.Event) {
		if e.Kind == history.RunStarted {
			runLog = e.LogPath
		}
	})})

	if runLog == "" || !strings.HasSuffix(runLog, "flow.log") {
		t.Fatalf("RunStarted LogPath = %q, want the run's flow.log", runLog)
	}
	if runLog != ld.Combined() {
		t.Fatalf("RunStarted LogPath = %q, want %q", runLog, ld.Combined())
	}
	_ = ld.Close()

	data, err := os.ReadFile(runLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "run/build") || !strings.Contains(string(data), "compiling") {
		t.Fatalf("flow.log missing the tagged line:\n%s", data)
	}

	// No file per call.
	entries, err := os.ReadDir(ld.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "flow.log" && e.Name() != "run.jsonl" {
			t.Fatalf("unexpected per-call log file: %s", e.Name())
		}
	}
}

func TestDoRunsAndReturnsItsValue(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		v, err := Do(ctx, "greet", func(ctx *Context) (string, error) {
			ctx.Summarize("said hello")
			return "hello", nil
		})
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		if v != "hello" {
			t.Fatalf("got %q, want %q", v, "hello")
		}
		return nil
	}, Options{})

	if res.ExitCode() != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode())
	}
	tr := res.Tasks["run/greet"]
	if tr == nil {
		t.Fatalf("no task recorded at run/greet; have: %v", res.Order)
	}
	if tr.Status != history.Succeeded || tr.Summary != "said hello" {
		t.Fatalf("got %+v", tr)
	}
}

func TestDoDefaultSummaryIsTheValueItself(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "identify", func(ctx *Context) (string, error) {
			return "dist/widget-linux", nil
		})
		return err
	}, Options{})
	if got := res.Tasks["run/identify"].Summary; got != "dist/widget-linux" {
		t.Fatalf("got %q, want the return value used as the summary", got)
	}
}

func TestDoDefaultSummaryForANonStringValueIsDone(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "barrier", func(ctx *Context) (bool, error) { return true, nil })
		return err
	}, Options{})
	if got := res.Tasks["run/barrier"].Summary; got != "done" {
		t.Fatalf("got %q, want %q", got, "done")
	}
}

func TestDoNestsUnderItsCaller(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "outer", func(ctx *Context) (bool, error) {
			_, err := Do(ctx, "inner", func(ctx *Context) (bool, error) {
				return true, nil
			})
			return true, err
		})
		return err
	}, Options{Name: "root"})

	inner := res.Tasks["root/outer/inner"]
	if inner == nil {
		t.Fatalf("no task at root/outer/inner; have: %v", res.Order)
	}
	if inner.Parent != "root/outer" {
		t.Fatalf("Parent = %q, want %q", inner.Parent, "root/outer")
	}
}

func TestDoErrorPropagatesToCaller(t *testing.T) {
	boom := errors.New("boom")
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "fail", func(ctx *Context) (int, error) {
			return 0, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("got err %v, want %v", err, boom)
		}
		return err
	}, Options{})

	if res.ExitCode() == 0 {
		t.Fatal("ExitCode = 0, want nonzero after a failure")
	}
	var sawFail bool
	for _, f := range res.Failed() {
		if f.Path == "run/fail" {
			sawFail = true
		}
	}
	if !sawFail {
		t.Fatalf("Failed() = %+v, want run/fail among them", res.Failed())
	}
}

func TestDoADownstreamCallIsSimplyNeverMade(t *testing.T) {
	var reached atomic.Bool
	runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "gate", func(ctx *Context) (bool, error) {
			return false, fmt.Errorf("gate closed")
		})
		if err != nil {
			return nil // the branch that would call "downstream" is simply not taken
		}
		_, _ = Do(ctx, "downstream", func(ctx *Context) (bool, error) {
			reached.Store(true)
			return true, nil
		})
		return nil
	}, Options{})

	if reached.Load() {
		t.Fatal("downstream ran even though the gate closed")
	}
}

func TestSkipCountsAsSuccessButIsReportedApart(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "maybe", func(ctx *Context) (string, error) {
			return "", Skip("nothing to do")
		})
		return err
	}, Options{})

	if res.ExitCode() != 0 {
		t.Fatalf("ExitCode = %d, want 0 for a skipped call", res.ExitCode())
	}
	tr := res.Tasks["run/maybe"]
	if tr.Status != history.Skipped || tr.Summary != "nothing to do" {
		t.Fatalf("got %+v", tr)
	}
}

func TestGoRunsConcurrentlyAndFutureGetWaits(t *testing.T) {
	var running atomic.Int32
	var sawBothConcurrently atomic.Bool
	work := func(ctx *Context) (string, error) {
		running.Add(1)
		defer running.Add(-1)
		time.Sleep(20 * time.Millisecond)
		if running.Load() >= 2 {
			sawBothConcurrently.Store(true)
		}
		return "ok", nil
	}

	res := runFor(t, func(ctx *Context) error {
		a := Go(ctx, "a", work)
		b := Go(ctx, "b", work)
		va, err := a.Get()
		if err != nil {
			return err
		}
		vb, err := b.Get()
		if err != nil {
			return err
		}
		if va != "ok" || vb != "ok" {
			t.Fatalf("got %q, %q", va, vb)
		}
		return nil
	}, Options{})

	if !sawBothConcurrently.Load() {
		t.Fatal("the two Go calls never overlapped")
	}
	if res.ExitCode() != 0 {
		t.Fatalf("ExitCode = %d", res.ExitCode())
	}
}

func TestConcurrencyBoundsGoCalls(t *testing.T) {
	var running, maxSeen atomic.Int32
	work := func(ctx *Context) (bool, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := maxSeen.Load()
			if n <= m || maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		return true, nil
	}

	runFor(t, func(ctx *Context) error {
		futures := make([]*Future[bool], 5)
		for i := range futures {
			futures[i] = Go(ctx, fmt.Sprintf("w%d", i), work)
		}
		for _, f := range futures {
			if _, err := f.Get(); err != nil {
				return err
			}
		}
		return nil
	}, Options{Concurrency: 2})

	if maxSeen.Load() > 2 {
		t.Fatalf("maxSeen = %d, want <= 2", maxSeen.Load())
	}
}

func TestCriticalFailureAbortsSiblingsInFlight(t *testing.T) {
	var sideRan atomic.Bool
	res := runFor(t, func(ctx *Context) error {
		side := Go(ctx, "side", func(ctx *Context) (bool, error) {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(2 * time.Second):
				sideRan.Store(true)
				return true, nil
			}
		})
		_, err := Do(ctx, "critical", func(ctx *Context) (bool, error) {
			return false, errors.New("boom")
		}, Critical())
		_, _ = side.Get()
		return err
	}, Options{})

	if sideRan.Load() {
		t.Fatal("the side call finished instead of being canceled by the critical failure")
	}
	if !res.Aborted {
		t.Fatal("Aborted = false, want true after a Critical failure")
	}
}

func TestFatalAbortsEvenWithoutCritical(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "fatal", func(ctx *Context) (bool, error) {
			return false, Fatal(errors.New("stop everything"))
		})
		return err
	}, Options{})

	if !res.Aborted {
		t.Fatal("Aborted = false, want true after a Fatal error")
	}
}

func TestStopOnErrorAbortsOnAnyFailure(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "ordinary", func(ctx *Context) (bool, error) {
			return false, errors.New("ordinary failure")
		})
		return err
	}, Options{StopOnError: true})

	if !res.Aborted {
		t.Fatal("Aborted = false, want true with StopOnError")
	}
}

func TestPanicIsRecoveredAsAFailure(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, err := Do(ctx, "boom", func(ctx *Context) (bool, error) {
			panic("kaboom")
		})
		return err
	}, Options{})

	tr := res.Tasks["run/boom"]
	if tr.Status != history.Failed || !strings.Contains(tr.Err.Error(), "kaboom") {
		t.Fatalf("got %+v", tr)
	}
	// The panic in one call must not have taken the process, or the rest of
	// the run, down with it.
	if res.Tasks["run"].Status != history.Failed {
		t.Fatalf("root status = %v, want Failed", res.Tasks["run"].Status)
	}
}

func TestTimeoutFailsTheCallAndLeavesTheRunAlone(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, _ = Do(ctx, "slow", func(ctx *Context) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		}, Timeout(20*time.Millisecond))
		_, err := Do(ctx, "after", func(ctx *Context) (bool, error) {
			return true, nil
		})
		return err
	}, Options{})

	slow := res.Tasks["run/slow"]
	if slow.Status != history.Failed || !strings.Contains(slow.Summary, "timed out") {
		t.Fatalf("got %+v", slow)
	}
	if res.Tasks["run/after"].Status != history.Succeeded {
		t.Fatal("a timeout in one call should not stop an unrelated later call")
	}
}

func TestDuplicateNamesUnderOneParentAreDisambiguated(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		for i := 0; i < 3; i++ {
			_, _ = Do(ctx, "item", func(ctx *Context) (bool, error) { return true, nil })
		}
		return nil
	}, Options{})

	for _, want := range []string{"run/item", "run/item#2", "run/item#3"} {
		if res.Tasks[want] == nil {
			t.Fatalf("missing task %q; have %v", want, res.Order)
		}
	}
}
