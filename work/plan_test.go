package work

import (
	"context"
	"testing"

	"github.com/m-d-brown/cascade/history"
)

func TestPlanRunIsJournaledButNeverRestorableOrFoundByLastRecord(t *testing.T) {
	store := history.NewMemoryStore()
	workflow := func(ctx *Context) error {
		_, err := Do(ctx, "work", func(ctx *Context) (string, error) {
			return "would-have-happened", nil
		})
		return err
	}
	res, err := NewRunner(errOnly(workflow), Options{
		Plan: true, Store: store, RunID: "plan1",
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tasks["run/work"].Status != history.Succeeded {
		t.Fatalf("got %+v", res.Tasks["run/work"])
	}

	j, _ := store.Load(context.Background())
	theRun, ok := j.Run("plan1")
	if !ok || !theRun.Plan {
		t.Fatalf("the plan run was not journaled as such: %+v", theRun)
	}
	// dot/flamegraph read exactly this: the trace must be there to draw.
	if len(theRun.Tasks) == 0 {
		t.Fatal("a plan run's trace must still be recorded for dot/flamegraph")
	}

	// But it must never be treated as real work: not by --continue, and not
	// by an app's own LastRecord freshness check.
	if _, ok := theRun.Restorable("run/work", ""); ok {
		t.Fatal("a plan run's record must never be Restorable")
	}
	var foundByLastRecord bool
	if _, err := NewRunner(errOnly(func(ctx *Context) error {
		_, _, foundByLastRecord = LastRecord[string](ctx, "work")
		return nil
	}), Options{Store: store, RunID: "real1"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if foundByLastRecord {
		t.Fatal("LastRecord must not find a plan run's record")
	}
}
