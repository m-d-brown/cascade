package history

import (
	"encoding/json"
	"sort"
)

// Flamegraph renders a run as Chrome Trace Event Format JSON
// (https://docs.google.com/document/d/1CvAClvFfyA5R-PhYUmn5OOQtYMH4h6I0nSsKchNAySU):
// one row per call, spanning its Started to Finished, viewable at
// chrome://tracing or https://ui.perfetto.dev. Drag the output in, zoom, and
// click a call for its exact times.
//
//	release flamegraph 2026-08-23T17-13-48 > trace.json
//
// This is a concurrency timeline, not a call-stack flame graph: a row shows
// when a call ran and for how long, not the tree it hung from. Two rows
// overlapping is what running two calls concurrently looks like here.
func Flamegraph(r Run) string {
	paths := make([]string, 0, len(r.Tasks))
	for path := range r.Tasks {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	label := r.Input
	if label == "" {
		label = r.ID
	}
	events := []traceEvent{
		{Ph: "M", Name: "process_name", PID: 1, Args: map[string]any{"name": label}},
	}
	for i, path := range paths {
		tid := i + 1
		events = append(events, traceEvent{Ph: "M", Name: "thread_name", PID: 1, TID: tid,
			Args: map[string]any{"name": path}})

		rec := r.Tasks[path]
		if rec.Started.IsZero() {
			continue // never ran: canceled before it began
		}
		dur := rec.Finished.Sub(rec.Started)
		if dur < 0 {
			dur = 0
		}
		args := map[string]any{"status": rec.Status.String()}
		if rec.Summary != "" {
			args["summary"] = rec.Summary
		}
		if rec.Error != "" {
			args["error"] = rec.Error
		}
		events = append(events, traceEvent{
			Name: path, Cat: "task", Ph: "X",
			TS:  rec.Started.Sub(r.Started).Microseconds(),
			Dur: dur.Microseconds(),
			PID: 1, TID: tid,
			Args: args,
		})
	}

	// Every field above is a string, an int or a map of them: nothing here
	// can hit one of the few things json.Marshal actually fails on (a chan,
	// a func, a cycle), so there is no error worth surfacing. Dot's own
	// callers rely on the same reasoning for a plain string return.
	data, _ := json.MarshalIndent(struct {
		TraceEvents []traceEvent `json:"traceEvents"`
	}{events}, "", "  ")
	return string(data)
}

// traceEvent is one entry in the Chrome Trace Event Format. "M" (metadata)
// events name a process or a thread; "X" (complete) events are the spans.
type traceEvent struct {
	Name string         `json:"name,omitempty"`
	Cat  string         `json:"cat,omitempty"`
	Ph   string         `json:"ph"`
	TS   int64          `json:"ts"`
	Dur  int64          `json:"dur,omitempty"`
	PID  int            `json:"pid"`
	TID  int            `json:"tid"`
	Args map[string]any `json:"args,omitempty"`
}
