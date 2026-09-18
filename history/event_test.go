package history

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLogEventsWritesOneJSONLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	obs := LogEvents(&buf)
	now := time.Now()

	obs.Handle(Event{Kind: RunStarted, Run: "r1", Time: now})
	obs.Handle(Event{Kind: TaskStarted, Run: "r1", Path: "release/build", Status: Running, Time: now})
	obs.Handle(Event{Kind: TaskFinished, Run: "r1", Path: "release/build", Status: Succeeded, Time: now,
		Result: &TaskResult{Status: Succeeded, Summary: "built", Duration: time.Second}})
	obs.Handle(Event{Kind: RunFinished, Run: "r1", Time: now,
		Outcome: &Result{Duration: 2 * time.Second, Counts: map[Status]int{Succeeded: 1}}})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4:\n%s", len(lines), buf.String())
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[3]), &last); err != nil {
		t.Fatal(err)
	}
	if last["event"] != "run-finished" || last["status"] != "ok" {
		t.Fatalf("got %+v", last)
	}
}

func TestLogEventsFailureAndAbortedStatus(t *testing.T) {
	var buf bytes.Buffer
	obs := LogEvents(&buf)
	obs.Handle(Event{Kind: RunFinished, Outcome: &Result{Aborted: true, AbortReason: "boom"}})
	if !strings.Contains(buf.String(), `"canceled"`) || !strings.Contains(buf.String(), "boom") {
		t.Fatalf("got %s", buf.String())
	}
}

func TestObserversFansOutToEachInOrder(t *testing.T) {
	var order []string
	a := newObserver(func(Event) { order = append(order, "a") })
	b := newObserver(func(Event) { order = append(order, "b") })
	Observers(a, b).Handle(Event{})
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("got %v", order)
	}
}

func TestEventKindStringAndMarshalText(t *testing.T) {
	for _, k := range []EventKind{RunStarted, TaskStarted, TaskLog, TaskStatus, TaskFinished, RunLog, RunFinished} {
		if k.String() == "unknown" {
			t.Errorf("EventKind %d strings as unknown", k)
		}
	}
	b, err := TaskFinished.MarshalText()
	if err != nil || string(b) != "task-finished" {
		t.Fatalf("got %q, %v", b, err)
	}
	if EventKind(99).String() != "unknown" {
		t.Fatal("an unrecognized kind should string as unknown")
	}
}
