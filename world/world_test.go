package world_test

import (
	"context"
	"errors"
	"testing"

	"github.com/m-d-brown/cascade/history"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
)

// runFor runs fn as the whole of a workflow and returns the result, for
// tests that need a real *work.Context to hand to world.Perform/Change.
func runFor(t *testing.T, fn func(ctx *work.Context) error) *history.Result {
	t.Helper()
	res, err := work.NewRunner(func(ctx *work.Context) (string, error) {
		return "", fn(ctx)
	}, work.Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRealPerformsTheEffect(t *testing.T) {
	var ran bool
	runFor(t, func(ctx *work.Context) error {
		v, err := world.Perform(ctx, world.Real(), world.Effect[int]{
			What:    "do the thing",
			Do:      func() (int, error) { ran = true; return 42, nil },
			Instead: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if v != 42 {
			t.Fatalf("got %v", v)
		}
		return nil
	})
	if !ran {
		t.Fatal("Do was never called under Real()")
	}
}

func TestDryRunNeverPerformsAndHandsBackInstead(t *testing.T) {
	var ran bool
	runFor(t, func(ctx *work.Context) error {
		v, err := world.Perform(ctx, world.DryRun(), world.Effect[int]{
			What:    "do the thing",
			Do:      func() (int, error) { ran = true; return 42, nil },
			Instead: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if v != -1 {
			t.Fatalf("got %v, want the Instead value", v)
		}
		return nil
	})
	if ran {
		t.Fatal("Do was called under DryRun()")
	}
}

func TestNilWorldDefaultsToReal(t *testing.T) {
	var ran bool
	runFor(t, func(ctx *work.Context) error {
		_, err := world.Perform(ctx, nil, world.Effect[any]{
			Do: func() (any, error) { ran = true; return nil, nil },
		})
		return err
	})
	if !ran {
		t.Fatal("a nil World should default to Real()")
	}
}

func TestChangeWrapsTheEffectsError(t *testing.T) {
	boom := errors.New("boom")
	runFor(t, func(ctx *work.Context) error {
		err := world.Change(ctx, world.Real(), "break something", func() error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("got %v, want it to wrap %v", err, boom)
		}
		return nil
	})
}

type fixedPrompter struct{ decision world.Decision }

func (f fixedPrompter) PromptApproval(context.Context, world.Request) (world.Decision, error) {
	return f.decision, nil
}

func TestConfirmSkipOnceLeavesTheEffectUnperformed(t *testing.T) {
	var ran bool
	w := world.Confirm(world.Real(), fixedPrompter{decision: world.SkipOnce})
	runFor(t, func(ctx *work.Context) error {
		_, err := world.Perform(ctx, w, world.Effect[string]{
			What:    "do it",
			Do:      func() (string, error) { ran = true; return "", nil },
			Instead: "skipped",
		})
		return err
	})
	if ran {
		t.Fatal("SkipOnce should not have let the effect run")
	}
}

func TestConfirmAbortRunStopsTheRun(t *testing.T) {
	w := world.Confirm(world.Real(), fixedPrompter{decision: world.AbortRun})
	res := runFor(t, func(ctx *work.Context) error {
		_, err := world.Perform(ctx, w, world.Effect[any]{Do: func() (any, error) { return nil, nil }})
		return err
	})
	if !res.Aborted {
		t.Fatal("AbortRun should have aborted the run")
	}
}

func TestConfirmApproveAllStopsAskingAfterTheFirst(t *testing.T) {
	calls := 0
	w := world.Confirm(world.Real(), promptCounter{n: &calls, decision: world.ApproveAll})
	runFor(t, func(ctx *work.Context) error {
		for i := 0; i < 3; i++ {
			if _, err := world.Perform(ctx, w, world.Effect[any]{Do: func() (any, error) { return nil, nil }}); err != nil {
				return err
			}
		}
		return nil
	})
	if calls != 1 {
		t.Fatalf("prompted %d times, want 1 (ApproveAll should cover the rest)", calls)
	}
}

type promptCounter struct {
	n        *int
	decision world.Decision
}

func (p promptCounter) PromptApproval(context.Context, world.Request) (world.Decision, error) {
	*p.n++
	return p.decision, nil
}

func TestPrompterRoundTripsThroughContext(t *testing.T) {
	want := fixedPrompter{decision: world.ApproveOnce}
	ctx := world.WithPrompter(context.Background(), want)
	if got := world.PrompterFromContext(ctx); got != want {
		t.Fatalf("got %v, want the prompter put in", got)
	}
}

func TestPrompterFromContextIsNilWhenUnset(t *testing.T) {
	if got := world.PrompterFromContext(context.Background()); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
	if got := world.PrompterFromContext(nil); got != nil { //nolint:staticcheck // a nil context is a caller mistake we tolerate rather than panic on
		t.Fatalf("got %v from a nil context, want nil", got)
	}
}
