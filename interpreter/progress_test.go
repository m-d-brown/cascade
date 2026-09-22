package interpreter

import (
	"context"
	"testing"
	"time"

	"github.com/m-d-brown/cascade/history"
	"github.com/m-d-brown/cascade/units"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
)

func TestReadProgressFoldsTheEventLog(t *testing.T) {
	logs, err := history.NewLogDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := mustParse(t, `
actions:
  first:  {run: "echo hello"}
  second: {run: "echo goodbye", needs: [first]}
`)
	if _, err := work.NewRunner(func(ctx *work.Context) (string, error) {
		return r.Run(ctx, world.Real())
	}, work.Options{Logs: logs, Observer: history.LogEvents(logs.Events())}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = logs.Close()

	rp, err := ReadProgress(logs.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rp.Finished {
		t.Error("RunProgress.Finished = false, want true")
	}
	if len(rp.Actions) != 2 {
		t.Fatalf("got %d actions, want 2: %+v", len(rp.Actions), rp.Actions)
	}
	if rp.Actions[0].Name != "first" || rp.Actions[1].Name != "second" {
		t.Fatalf("actions out of start order: %s, %s", rp.Actions[0].Name, rp.Actions[1].Name)
	}
	for _, a := range rp.Actions {
		if a.Status != history.Succeeded {
			t.Errorf("%s status = %v, want ok", a.Name, a.Status)
		}
	}
	if rp.Actions[0].Detail != "hello" {
		t.Errorf("first detail = %q, want hello", rp.Actions[0].Detail)
	}
}

func TestReadProgressMissingLog(t *testing.T) {
	if _, err := ReadProgress(t.TempDir()); err == nil {
		t.Fatal("want an error for a directory with no run.jsonl")
	}
}

func TestChildName(t *testing.T) {
	for _, tc := range []struct {
		root, task, want string
		ok               bool
	}{
		{"cascade", "cascade/build", "build", true},
		{"cascade", "cascade/build/sub", "", false},
		{"cascade", "cascade", "", false},
		{"cascade", "other/build", "", false},
	} {
		got, ok := childName(tc.root, tc.task)
		if got != tc.want || ok != tc.ok {
			t.Errorf("childName(%q,%q) = %q,%v want %q,%v", tc.root, tc.task, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProgressInterval(t *testing.T) {
	for _, tc := range []struct {
		a    Action
		want time.Duration
	}{
		{Action{}, 2 * time.Second},
		{Action{Progress: "x"}, 15 * time.Second},
		{Action{Progress: "x", ProgressEvery: "3s"}, 3 * time.Second},
		{Action{ProgressEvery: "500ms"}, time.Second},  // floored: a hot-loop probe is a mistake
		{Action{ProgressEvery: "0s"}, 2 * time.Second}, // not positive: falls back to the default
	} {
		if got := progressInterval(tc.a); got != tc.want {
			t.Errorf("progressInterval(%+v) = %v, want %v", tc.a, got, tc.want)
		}
	}
}

func TestProgressProbe(t *testing.T) {
	w := units.NewWatcher()

	line, err := progressProbe(Action{Progress: "printf 'one\\ntwo\\n'"}, w)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if line != "two" {
		t.Fatalf("progress command probe = %q, want two", line)
	}

	// no Progress: falls back to the command's own output tail (empty here)
	if line, _ := progressProbe(Action{}, w)(context.Background()); line != "" {
		t.Fatalf("default probe with no output = %q, want empty", line)
	}
}
