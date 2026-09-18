package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mdbrown/cascade/history"
)

func TestSummarySucceededVerdict(t *testing.T) {
	res := &history.Result{
		Order: []string{"release/build"},
		Tasks: map[string]*history.TaskResult{
			"release/build": {Path: "release/build", Status: history.Succeeded, Summary: "built", Duration: time.Second},
		},
		Counts: map[history.Status]int{history.Succeeded: 1},
	}
	var buf bytes.Buffer
	Summary(&buf, res)
	out := buf.String()
	if !strings.Contains(out, "release/build") || !strings.Contains(out, "all calls succeeded") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestSummaryFailedVerdictShowsAPanelPerFailure(t *testing.T) {
	res := &history.Result{
		Order: []string{"release/build"},
		Tasks: map[string]*history.TaskResult{
			"release/build": {Path: "release/build", Status: history.Failed, Err: errors.New("boom"), LogPath: "/logs/release-build.log"},
		},
		Counts: map[history.Status]int{history.Failed: 1},
	}
	var buf bytes.Buffer
	Summary(&buf, res)
	out := buf.String()
	for _, want := range []string{"release/build", "boom", "/logs/release-build.log", "run failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSummaryAbortedVerdict(t *testing.T) {
	res := &history.Result{Aborted: true, AbortReason: "critical call failed", Counts: map[history.Status]int{}}
	var buf bytes.Buffer
	Summary(&buf, res)
	if !strings.Contains(buf.String(), "run aborted: critical call failed") {
		t.Fatalf("got:\n%s", buf.String())
	}
}
