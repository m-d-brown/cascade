package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/world"
)

func TestPlainWritesOneLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, nil, false)
	now := time.Now()

	p.Handle(history.Event{Kind: history.RunStarted, Run: "r1", Time: now})
	p.Handle(history.Event{Kind: history.TaskStarted, Path: "release/build", Status: history.Running, Time: now})
	p.Handle(history.Event{Kind: history.TaskLog, Path: "release/build", Message: "compiling", Time: now})
	p.Handle(history.Event{Kind: history.TaskStatus, Path: "release/build", Message: "not shown unless verbose", Time: now})
	p.Handle(history.Event{Kind: history.TaskFinished, Path: "release/build", Status: history.Succeeded, Time: now,
		Result: &history.TaskResult{Status: history.Succeeded, Summary: "built", Duration: time.Second}})
	p.Handle(history.Event{Kind: history.RunFinished, Time: now, Outcome: &history.Result{Duration: 2 * time.Second}})

	out := buf.String()
	for _, want := range []string{"run r1 started", "release/build", "compiling", "built", "run finished in"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not shown unless verbose") {
		t.Errorf("a status line leaked through without --verbose:\n%s", out)
	}
}

func TestPlainVerboseShowsStatusLines(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, nil, true)
	p.Handle(history.Event{Kind: history.TaskStatus, Path: "a", Message: "42%"})
	if !strings.Contains(buf.String(), "42%") {
		t.Fatalf("verbose output missing the status line:\n%s", buf.String())
	}
}

func TestPlainPromptApprovalReadsAChoice(t *testing.T) {
	var out bytes.Buffer
	p := NewPlain(&out, strings.NewReader("n\n"), false)
	dec, err := p.PromptApproval(context.Background(), world.Request{Task: "release/publish", Target: "upload dist/"})
	if err != nil {
		t.Fatal(err)
	}
	if dec != world.SkipOnce {
		t.Fatalf("got %v, want SkipOnce", dec)
	}
	if !strings.Contains(out.String(), "release/publish") {
		t.Fatalf("prompt did not name the task:\n%s", out.String())
	}
}
