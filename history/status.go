// Package history is what a call or a workflow run produced, and the ways
// to work with it after the fact: the state journal ([Store], [Journal],
// [Record]), the on-disk log directory ([LogDir]), the DOT and flame-graph
// renderers, and the events a live run emits ([Event], [Observer]). Nothing
// here is used by the code inside a call — see
// [github.com/mdbrown/cascade/work] for that. The runner writes to a
// [Store] and a [LogDir] as a run proceeds; everything else here is touched
// only by whatever renders, stores, or reports on a run from outside it:
// the cli package's `dot`, `flamegraph`, `state` and `runs` subcommands,
// and the ui package's live and plain displays.
package history

import "fmt"

// Status is the state of one call in a run.
type Status uint8

const (
	// Pending is the zero value: a call something has heard of but that has
	// not reached a status yet. The runner never records it — a call's first
	// event already carries Running, Resumed or Canceled — so it stands only
	// for the absence of any of the others. A call started concurrently and
	// still waiting for a concurrency slot has no status and no event at all
	// until its slot comes free.
	Pending Status = iota
	// Running means the call is executing.
	Running
	// Succeeded means the call returned no error.
	Succeeded
	// Skipped means the call ran and decided to do nothing (Skip).
	Skipped
	// Resumed means the run is continuing an earlier one, which already made
	// this call: its recorded output stands in for calling it again.
	Resumed
	// Failed means the call returned an error, or panicked.
	Failed
	// Canceled means the run was aborted before this call could run.
	Canceled
)

var statusNames = map[Status]string{
	Pending:   "pending",
	Running:   "running",
	Succeeded: "ok",
	Skipped:   "skipped",
	Resumed:   "resumed",
	Failed:    "failed",
	Canceled:  "canceled",
}

func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return "unknown"
}

// Terminal reports whether the call will not change state again.
func (s Status) Terminal() bool {
	switch s {
	case Succeeded, Skipped, Resumed, Failed, Canceled:
		return true
	}
	return false
}

// OK reports whether the call reached a terminal state that counts as
// success — the same vocabulary a run itself ends in.
func (s Status) OK() bool {
	switch s {
	case Succeeded, Skipped, Resumed:
		return true
	}
	return false
}

// Symbol is a one-rune glyph for terminal output.
func (s Status) Symbol() string {
	switch s {
	case Succeeded:
		return "✓"
	case Resumed:
		return "⤾"
	case Skipped:
		return "○"
	case Failed:
		return "✗"
	case Canceled:
		return "—"
	case Running:
		return "•"
	}
	return " "
}

// MarshalText implements encoding.TextMarshaler so statuses are readable in
// the journal.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Status) UnmarshalText(b []byte) error {
	for status, name := range statusNames {
		if name == string(b) {
			*s = status
			return nil
		}
	}
	return fmt.Errorf("unknown status %q", b)
}
