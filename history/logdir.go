package history

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// logDirLayout is how a run's log directory is named. The run has no id of
// its own, so this also names the run.
const logDirLayout = "2006-01-02T15-04-05"

// LogDir is one run's logs on disk: a single text log with every line in the
// order it was written, and the run's event log (the machine-readable index)
// alongside it, in the layout of "~/backup-logs/<run>/".
//
//	2026-08-23T16-59-01/
//	  flow.log       every line, each tagged with the call it came from
//	  run.jsonl      one JSON object per event: what ran, what it said, how it ended
//
// There is one text log, not a file per call: which call a line belongs to
// is the third column of flow.log, and run.jsonl is the structured index for
// anything that needs to pick a call's lines out again.
//
// The runner writes into it as a run proceeds, via [LogDir.WriteCombined]
// for flow.log and [LogDir.Events] for the event log, and `logs` reads
// flow.log back afterward. A nil *LogDir is a valid no-op: a run with
// nowhere to write its logs.
type LogDir struct {
	Dir string

	mu       sync.Mutex
	combined *os.File
	events   *os.File
}

// NewLogDir creates a directory for one run under base, named for the time
// it started, and returns it. The name is made unique, so two runs started
// in the same second never write into each other.
func NewLogDir(base string) (*LogDir, error) {
	stamp := time.Now().Format(logDirLayout)
	dir := filepath.Join(base, stamp)
	for n := 2; ; n++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", stamp, n))
	}
	return openLogDir(dir)
}

// Run is the name of this run: the log directory's own name, which is also
// what the journal calls the run.
func (d *LogDir) Run() string {
	if d == nil {
		return ""
	}
	return filepath.Base(d.Dir)
}

// Events returns the writer for the run's event log, creating it on first
// use. Hand it to [LogEvents].
func (d *LogDir) Events() io.Writer {
	if d == nil {
		return io.Discard
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.events != nil {
		return d.events
	}
	f, err := os.OpenFile(filepath.Join(d.Dir, "run.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard
	}
	d.events = f
	return f
}

// openLogDir creates dir (and parents) and returns it.
func openLogDir(dir string) (*LogDir, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	combined, err := os.OpenFile(filepath.Join(dir, "flow.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open combined log: %w", err)
	}
	return &LogDir{Dir: dir, combined: combined}, nil
}

// Combined is the path of the run's one text log, or "" for a nil *LogDir.
// Every call's lines are in it, so this is [Context.LogPath] and
// [TaskResult.LogPath] for every call alike.
func (d *LogDir) Combined() string {
	if d == nil {
		return ""
	}
	return filepath.Join(d.Dir, "flow.log")
}

// WriteCombined appends one line to flow.log, tagged with the call it came
// from. A nil *LogDir does nothing.
func (d *LogDir) WriteCombined(callPath string, level slog.Level, msg string, when time.Time) {
	if d == nil || d.combined == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(d.combined, "%s %-5s %-24s %s\n", when.Format("15:04:05.000"), level, callPath, msg)
}

// Close closes the log files.
func (d *LogDir) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	for _, f := range []**os.File{&d.combined, &d.events} {
		if *f != nil {
			if cerr := (*f).Close(); cerr != nil && err == nil {
				err = cerr
			}
			*f = nil
		}
	}
	return err
}
