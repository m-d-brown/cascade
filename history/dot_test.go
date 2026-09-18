package history

import (
	"strings"
	"testing"
)

func exampleRun() Run {
	return Run{
		ID: "r1", Input: "v1.0",
		Tasks: map[string]Record{
			"release":             {Path: "release", Status: Succeeded, Doc: "ship it"},
			"release/build-linux": {Path: "release/build-linux", Status: Succeeded, Tag: "build"},
			"release/publish":     {Path: "release/publish", Status: Failed},
		},
	}
}

func TestDotDrawsABoxPerCallAndAnEdgePerParent(t *testing.T) {
	out := Dot(exampleRun(), DotOptions{Title: "release", Cluster: true})
	if out == "" {
		t.Fatal("Dot returned an empty string")
	}
	for _, want := range []string{"release", "build-linux", "publish", "digraph"} {
		if !strings.Contains(out, want) {
			t.Errorf("Dot output missing %q:\n%s", want, out)
		}
	}
}

func TestFlamegraphIsValidTraceEventJSON(t *testing.T) {
	run := exampleRun()
	run.Tasks["release"] = Record{Path: "release", Status: Succeeded}
	out := Flamegraph(run)
	if !strings.Contains(out, "traceEvents") {
		t.Fatalf("not trace event JSON:\n%s", out)
	}
}
