package work

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/mdbrown/cascade/history"
)

func TestContinueResumesSucceededCallsAndRerunsTheRest(t *testing.T) {
	store := history.NewMemoryStore()
	var firstRan, secondRan atomic.Int32

	workflow := func(secondShouldFail bool) func(ctx *Context) error {
		return func(ctx *Context) error {
			_, err := Do(ctx, "first", func(ctx *Context) (string, error) {
				firstRan.Add(1)
				return "first-value", nil
			})
			if err != nil {
				return err
			}
			_, err = Do(ctx, "second", func(ctx *Context) (string, error) {
				secondRan.Add(1)
				if secondShouldFail {
					return "", errors.New("boom")
				}
				return "second-value", nil
			})
			return err
		}
	}

	res1, err := NewRunner(errOnly(workflow(true)), Options{Store: store, RunID: "run1"}).Run(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if res1.Tasks["run/first"].Status != history.Succeeded {
		t.Fatalf("first: got %+v", res1.Tasks["run/first"])
	}
	if firstRan.Load() != 1 || secondRan.Load() != 1 {
		t.Fatalf("firstRan=%d secondRan=%d after run1", firstRan.Load(), secondRan.Load())
	}

	res2, err := NewRunner(errOnly(workflow(false)), Options{Store: store, RunID: "run2", Continue: "run1"}).Run(context.Background())
	if err != nil {
		t.Fatalf("continued run: %v", err)
	}
	if res2.Tasks["run/first"].Status != history.Resumed {
		t.Fatalf("first on continue: got %+v", res2.Tasks["run/first"])
	}
	if firstRan.Load() != 1 {
		t.Fatalf("firstRan=%d after continuing; the resumed call must not run again", firstRan.Load())
	}
	if secondRan.Load() != 2 {
		t.Fatalf("secondRan=%d after continuing; the failed call must run again", secondRan.Load())
	}
	if res2.Tasks["run/second"].Status != history.Succeeded {
		t.Fatalf("second on continue: got %+v", res2.Tasks["run/second"])
	}
}

func TestSecretCallsAreNeverResumed(t *testing.T) {
	store := history.NewMemoryStore()
	var ran atomic.Int32
	workflow := func(ctx *Context) error {
		_, err := Do(ctx, "password", func(ctx *Context) (string, error) {
			ran.Add(1)
			return "hunter2", nil
		}, Secret())
		return err
	}

	if _, err := NewRunner(errOnly(workflow), Options{Store: store, RunID: "run1"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := NewRunner(errOnly(workflow), Options{Store: store, RunID: "run2", Continue: "run1"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tasks["run/password"].Status != history.Succeeded {
		t.Fatalf("a secret call must run again rather than resume: got %+v", res.Tasks["run/password"])
	}
	if ran.Load() != 2 {
		t.Fatalf("ran=%d, want 2", ran.Load())
	}

	// And its value must never have reached the store at all.
	j, _ := store.Load(context.Background())
	run1, _ := j.Run("run1")
	if rec := run1.Tasks["run/password"]; rec.HasValue || rec.Value != nil {
		t.Fatalf("a secret record was written with a value: %+v", rec)
	}
}

func TestConfigChangeInvalidatesResumption(t *testing.T) {
	store := history.NewMemoryStore()
	workflow := func(cfg string) func(ctx *Context) error {
		return func(ctx *Context) error {
			_, err := Do(ctx, "configured", func(ctx *Context) (string, error) {
				return cfg, nil
			}, Config(cfg))
			return err
		}
	}
	if _, err := NewRunner(errOnly(workflow("v1")), Options{Store: store, RunID: "run1"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := NewRunner(errOnly(workflow("v2")), Options{Store: store, RunID: "run2", Continue: "run1"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tasks["run/configured"].Status != history.Succeeded {
		t.Fatalf("a changed Config must not resume: got %+v", res.Tasks["run/configured"])
	}
}

func TestLastRecordFindsAPreviousRunWithoutContinue(t *testing.T) {
	store := history.NewMemoryStore()
	if _, err := NewRunner(errOnly(func(ctx *Context) error {
		_, err := Do(ctx, "backup", func(ctx *Context) (string, error) {
			return "/backups/2026-08-28", nil
		}, Config("daily"))
		return err
	}), Options{Store: store, RunID: "yesterday"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var found bool
	var value string
	if _, err := NewRunner(errOnly(func(ctx *Context) error {
		v, meta, ok := LastRecord[string](ctx, "backup", Config("daily"))
		found, value = ok, v
		if !meta.HasValue {
			t.Fatal("HasValue = false, want true")
		}
		return nil
	}), Options{Store: store, RunID: "today"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !found {
		t.Fatal("LastRecord did not find yesterday's backup")
	}
	if value != "/backups/2026-08-28" {
		t.Fatalf("value = %q", value)
	}
}

func TestLastRecordIsFalseWithoutAStore(t *testing.T) {
	var ok bool
	if _, err := NewRunner(errOnly(func(ctx *Context) error {
		_, _, ok = LastRecord[string](ctx, "anything")
		return nil
	}), Options{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("LastRecord found something with no Store configured")
	}
}
