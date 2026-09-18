package history

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"
)

// EventKind discriminates [Event].
type EventKind uint8

const (
	// RunStarted is emitted once, before the run's root call begins. Unlike
	// a declared graph, a run built from ordinary function calls has no list
	// of calls to hand over up front — they are discovered as the run makes
	// them, each announced by its own TaskStarted.
	RunStarted EventKind = iota
	// TaskStarted is emitted once when a call begins: its Status is Running
	// for one that is executing, Resumed for one whose recorded result stood
	// in, or Canceled for one the run had already aborted past. A call
	// started concurrently emits nothing while it waits for a concurrency
	// slot.
	TaskStarted
	// TaskLog carries a durable log line from a call.
	TaskLog
	// TaskStatus carries a transient progress line from a call: the latest
	// one is worth displaying, the history is not.
	TaskStatus
	// TaskFinished is emitted when a call reaches a terminal status.
	TaskFinished
	// RunLog carries a log line about the run itself rather than any one
	// call.
	RunLog
	// RunFinished is emitted once, after the run's root call has returned.
	RunFinished
)

var eventNames = map[EventKind]string{
	RunStarted:   "run-started",
	TaskStarted:  "task-started",
	TaskLog:      "task-log",
	TaskStatus:   "task-status",
	TaskFinished: "task-finished",
	RunLog:       "run-log",
	RunFinished:  "run-finished",
}

// String names the kind, as the run's event log writes it.
func (k EventKind) String() string {
	if n, ok := eventNames[k]; ok {
		return n
	}
	return "unknown"
}

// MarshalText implements encoding.TextMarshaler, so an event log is readable.
func (k EventKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// Event is a runtime notification. Observers receive events from multiple
// goroutines and must be safe for concurrent use.
type Event struct {
	Kind EventKind
	Time time.Time
	// Run is the id of the run this event belongs to.
	Run string
	// Path is the full path of the call this event is about — empty for
	// RunStarted, RunLog and RunFinished.
	Path string
	// Parent is Path's immediate parent, or "" for a top-level call. Set on
	// TaskStarted, which is what lets an observer build the call tree as it
	// happens rather than being handed it up front.
	Parent string
	// Doc and Tag are set on TaskStarted, from a call's Doc and Tag options.
	Doc     string
	Tag     string
	Status  Status
	Level   slog.Level
	Message string
	// LogPath is the run's one text log — flow.log, every call's lines in it,
	// each tagged with its call path. Set on RunStarted so an observer can
	// read a call's lines out of it while the run is still going. Empty for a
	// run with nowhere to write logs.
	LogPath string

	// Result is the call's result, set on TaskFinished.
	Result *TaskResult
	// Outcome is the whole run's result, set on RunFinished.
	Outcome *Result
}

// Observer consumes run events. Implementations must be safe for concurrent
// use; [multiObserver] fans out to several.
type Observer interface {
	Handle(Event)
}

type syncObserver struct {
	mu sync.Mutex
	f  func(Event)
}

func (o *syncObserver) Handle(e Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.f(e)
}

// newObserver adapts a function to [Observer]. Calls are serialized, so the
// function need not be safe for concurrent use itself.
func newObserver(f func(Event)) Observer { return &syncObserver{f: f} }

// Observers fans every event out to each observer in turn, so a run can be
// watched and written down at the same time:
//
//	history.Observers(live, history.LogEvents(f))
func Observers(all ...Observer) Observer { return multiObserver(all) }

// LogEvents returns an observer that writes one JSON object per line: the
// standard record of what a run did, in the order it happened.
//
//	{"time":"…","run":"2026-08-23T16-59-01","event":"task-finished","task":"build-linux","status":"ok","message":"built …","duration":"1.4s"}
//
// Every run writes one beside its logs, so what happened is answerable after
// the fact by something other than a person reading a terminal.
func LogEvents(w io.Writer) Observer {
	enc := json.NewEncoder(w)
	return newObserver(func(e Event) {
		line := eventLine{
			Time:    e.Time.Format(time.RFC3339Nano),
			Run:     e.Run,
			Event:   e.Kind.String(),
			Task:    e.Path,
			Message: e.Message,
		}
		if e.Level >= slog.LevelWarn && (e.Kind == TaskLog || e.Kind == RunLog) {
			line.Level = e.Level.String()
		}
		switch e.Kind {
		case TaskFinished:
			if e.Result != nil {
				line.Status = e.Result.Status.String()
				line.Message = e.Result.Summary
				line.Duration = e.Result.Duration.Round(time.Millisecond).String()
				if e.Result.Err != nil {
					line.Error = e.Result.Err.Error()
				}
			}
		case RunFinished:
			if e.Outcome != nil {
				line.Status = runStatus(e.Outcome)
				line.Duration = e.Outcome.Duration.Round(time.Millisecond).String()
				line.Counts = map[string]int{}
				for status, n := range e.Outcome.Counts {
					line.Counts[status.String()] = n
				}
				if e.Outcome.Aborted {
					line.Message = e.Outcome.AbortReason
				}
			}
		default:
			if e.Status != Pending {
				line.Status = e.Status.String()
			}
		}
		// An Observer has no error to return; a line the encoder could not
		// write is no different from one nobody was watching for.
		_ = enc.Encode(line)
	})
}

// eventLine is one line of the run's event log.
type eventLine struct {
	Time     string         `json:"time"`
	Run      string         `json:"run,omitempty"`
	Event    string         `json:"event"`
	Task     string         `json:"task,omitempty"`
	Status   string         `json:"status,omitempty"`
	Level    string         `json:"level,omitempty"`
	Message  string         `json:"message,omitempty"`
	Duration string         `json:"duration,omitempty"`
	Error    string         `json:"error,omitempty"`
	Counts   map[string]int `json:"counts,omitempty"`
}

func runStatus(res *Result) string {
	switch {
	case res.Aborted:
		return Canceled.String()
	case res.ExitCode() != 0:
		return Failed.String()
	}
	return Succeeded.String()
}

// multiObserver fans an event out to several observers, in order.
type multiObserver []Observer

// Handle implements [Observer].
func (m multiObserver) Handle(e Event) {
	for _, o := range m {
		if o != nil {
			o.Handle(e)
		}
	}
}
