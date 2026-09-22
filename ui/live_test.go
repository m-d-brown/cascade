package ui

import (
	"os"
	"testing"
	"time"

	"github.com/m-d-brown/cascade/history"
)

func TestModelApplyBuildsRowsAsCallsAreDiscovered(t *testing.T) {
	m := newModel(nil)
	now := time.Now()

	m.apply(history.Event{Kind: history.RunStarted, Time: now})
	if len(m.rows) != 0 {
		t.Fatalf("RunStarted should start with no rows; got %d", len(m.rows))
	}

	m.apply(history.Event{Kind: history.TaskStarted, Path: "release", Time: now, Status: history.Running})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release/build", Parent: "release", Time: now, Status: history.Running})
	if len(m.rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(m.rows))
	}
	if r := m.row("release/build"); r == nil || r.parent != "release" || r.status != history.Running {
		t.Fatalf("release/build row = %+v", r)
	}

	m.apply(history.Event{Kind: history.TaskLog, Path: "release/build", Message: "compiling"})
	if r := m.row("release/build"); r.message != "compiling" {
		t.Fatalf("message = %q, want %q", r.message, "compiling")
	}

	m.apply(history.Event{Kind: history.TaskFinished, Path: "release/build", Status: history.Succeeded,
		Result: &history.TaskResult{Status: history.Succeeded, Summary: "built", Duration: 2 * time.Second}})
	r := m.row("release/build")
	if r.status != history.Succeeded || r.message != "built" || r.elapsed != 2*time.Second {
		t.Fatalf("finished row = %+v", r)
	}
	if m.counts[history.Succeeded] != 1 {
		t.Fatalf("counts = %+v", m.counts)
	}
}

func TestModelApplyIgnoresLogLinesForAFinishedRow(t *testing.T) {
	m := newModel(nil)
	m.apply(history.Event{Kind: history.TaskStarted, Path: "a", Status: history.Running})
	m.apply(history.Event{Kind: history.TaskFinished, Path: "a", Status: history.Succeeded,
		Result: &history.TaskResult{Status: history.Succeeded, Summary: "done"}})
	// A straggler log line arriving after the row already reported its
	// result must not overwrite the summary just shown for it.
	m.apply(history.Event{Kind: history.TaskLog, Path: "a", Message: "late line"})
	if r := m.row("a"); r.message != "done" {
		t.Fatalf("message = %q, want the terminal summary to stand", r.message)
	}
}

func TestModelApplyRunFinishedQuits(t *testing.T) {
	m := newModel(nil)
	cmd := m.apply(history.Event{Kind: history.RunFinished, Outcome: &history.Result{}})
	if !m.finished {
		t.Fatal("finished = false")
	}
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
}

func TestWantKeepUpSkipsBrowseWhenExitWhenDoneIsSet(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	// exitWhenDone wins regardless of what's driving input, even an *os.File
	// that IsTerminal would call a terminal, were this one.
	if wantKeepUp(r, true) {
		t.Fatal("exitWhenDone=true should never keep the display up")
	}
	if wantKeepUp(nil, true) {
		t.Fatal("exitWhenDone=true should never keep the display up, even given no input at all")
	}
}

func TestWantKeepUpIsFalseForNonTerminalInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	// A pipe is an *os.File but not a terminal (the situation piped or
	// tested input is actually in), so there's nobody to drive a browse.
	if wantKeepUp(r, false) {
		t.Fatal("a pipe is not a terminal; keepUp should be false")
	}
}

func TestStartMessageNamesTheInitialStatus(t *testing.T) {
	cases := map[history.Status]string{
		history.Running:  "running",
		history.Resumed:  "resumed",
		history.Canceled: "canceled",
	}
	for status, want := range cases {
		if got := startMessage(status); got != want {
			t.Errorf("startMessage(%v) = %q, want %q", status, got, want)
		}
	}
}
