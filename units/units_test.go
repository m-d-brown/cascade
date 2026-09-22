package units_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/m-d-brown/cascade/history"
	"github.com/m-d-brown/cascade/units"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
)

func runFor(t *testing.T, root func(ctx *work.Context) error) *history.Result {
	t.Helper()
	rootFn := func(ctx *work.Context) (string, error) { return "", root(ctx) }
	res, err := work.NewRunner(rootFn, work.Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRunExecutesARealCommand(t *testing.T) {
	var res units.ExecResult
	var callErr error
	runFor(t, func(ctx *work.Context) error {
		res, callErr = units.Run(ctx, world.Real(), units.Cmd{Path: "echo", Args: []string{"hello"}})
		return callErr
	})
	if callErr != nil {
		t.Fatal(callErr)
	}
	if res.Output() != "hello" {
		t.Fatalf("got %q", res.Output())
	}
}

func TestRunUnderDryRunNeverExecutesAndReturnsInstead(t *testing.T) {
	var res units.ExecResult
	runFor(t, func(ctx *work.Context) error {
		var err error
		res, err = units.Run(ctx, world.DryRun(), units.Cmd{
			Path: "definitely-not-a-real-command-xyz", Instead: []string{"fake output"},
		})
		return err
	})
	if !res.DryRun || res.Output() != "fake output" {
		t.Fatalf("got %+v", res)
	}
}

func TestRunFailsOnNonZeroExit(t *testing.T) {
	var callErr error
	runFor(t, func(ctx *work.Context) error {
		_, callErr = units.Run(ctx, world.Real(), units.Cmd{Path: "sh", Args: []string{"-c", "exit 3"}})
		return nil
	})
	if callErr == nil {
		t.Fatal("expected an error for a nonzero exit")
	}
}

func TestRunAllowExitTreatsListedCodesAsSuccess(t *testing.T) {
	var callErr error
	runFor(t, func(ctx *work.Context) error {
		_, callErr = units.Run(ctx, world.Real(), units.Cmd{Path: "sh", Args: []string{"-c", "exit 1"}, AllowExit: []int{1}})
		return nil
	})
	if callErr != nil {
		t.Fatalf("got %v, want nil (exit 1 is allow-listed)", callErr)
	}
}

type fakeSource struct {
	values map[string]string
	reads  int
}

func (f *fakeSource) ID() string { return "fake" }
func (f *fakeSource) Read(ctx *work.Context, w world.World, refs []string) (map[string]string, error) {
	f.reads++
	out := map[string]string{}
	for _, ref := range refs {
		v, ok := f.values[ref]
		if !ok {
			return nil, fmt.Errorf("no value for %s", ref)
		}
		out[ref] = v
	}
	return out, nil
}

func TestSecretReturnsTheValue(t *testing.T) {
	src := &fakeSource{values: map[string]string{"ref-a": "s3cr3t"}}
	var got string
	runFor(t, func(ctx *work.Context) error {
		var err error
		got, err = units.Secret(ctx, world.Real(), src, "password", "ref-a")
		return err
	})
	if got != "s3cr3t" {
		t.Fatalf("got %q", got)
	}
}

func TestSecretsFetchesEveryRefInOneCall(t *testing.T) {
	src := &fakeSource{values: map[string]string{"ref-a": "va", "ref-b": "vb"}}
	var got map[string]string
	runFor(t, func(ctx *work.Context) error {
		var err error
		got, err = units.Secrets(ctx, world.Real(), src, "secrets", map[string]string{"a": "ref-a", "b": "ref-b"})
		return err
	})
	if got["a"] != "va" || got["b"] != "vb" {
		t.Fatalf("got %+v", got)
	}
	if src.reads != 1 {
		t.Fatalf("Read called %d times, want 1", src.reads)
	}
}

func TestSecretIsNeverResumed(t *testing.T) {
	src := &fakeSource{values: map[string]string{"ref-a": "s3cr3t"}}
	memStore := history.NewMemoryStore()
	workflow := func(ctx *work.Context) (string, error) {
		return units.Secret(ctx, world.Real(), src, "password", "ref-a")
	}
	if _, err := work.NewRunner(workflow, work.Options{Store: memStore, RunID: "r1"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := work.NewRunner(workflow, work.Options{Store: memStore, RunID: "r2", Continue: "r1"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.reads != 2 {
		t.Fatalf("Read called %d times across both runs, want 2 (a secret must never be resumed)", src.reads)
	}
}

func TestSecretFailsWhenTheSourceReturnsNothing(t *testing.T) {
	src := &fakeSource{values: map[string]string{}}
	var callErr error
	runFor(t, func(ctx *work.Context) error {
		_, callErr = units.Secret(ctx, world.Real(), src, "password", "missing")
		return nil
	})
	if callErr == nil {
		t.Fatal("expected an error for a missing secret")
	}
}
