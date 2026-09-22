package history

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is one call's outcome as written to the state journal.
type Record struct {
	Path        string `json:"path"`
	Doc         string `json:"doc,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Status      Status `json:"status"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	// Value is what the call produced, if it is something the journal can
	// carry, and the call was not marked Secret.
	Value any `json:"value,omitempty"`
	// HasValue distinguishes "produced nothing recordable" from "produced
	// the zero value", since Value omits either one the same way as JSON.
	HasValue bool `json:"hasValue,omitempty"`
	// Partial marks a record whose value could not be written because the
	// call produced something JSON cannot carry, so it cannot be stood in
	// for later.
	Partial bool `json:"partial,omitempty"`
	// Secret marks a call whose value is never written here, whatever it
	// produced. The record still says the call happened, when, and how it
	// ended; only the value itself is withheld.
	Secret   bool      `json:"secret,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Duration Duration  `json:"duration,omitempty"`
}

// worked reports whether the record is of work that happened: either this
// call ran and succeeded, or it was carried over from a run where it did.
func (rec Record) worked() bool {
	return rec.Status == Succeeded || rec.Status == Resumed
}

// Run is one execution of a workflow, as the journal remembers it: what was
// asked for, what has happened so far, and whether it ever finished.
//
// It is written when the run starts and rewritten every time a call reaches
// a terminal status, so a run interrupted by ^C, a power cut, or a closed
// laptop lid leaves behind exactly what it had got done. Such a run keeps
// the status Running rather than ever reaching one of the others, which is
// how `--continue last` picks it out among the journal's runs, and how the
// engine can carry it on later.
type Run struct {
	// ID names the run and its log directory.
	ID string `json:"id"`
	// Status is Running until the run ends, then Succeeded, Failed or
	// Canceled: the same vocabulary a call ends in.
	Status Status `json:"status"`
	// Input is a one-line description of what this run was for, such as the
	// version being released. It is set once when the run starts and never
	// overwritten by how it ends. Empty for a workflow with nothing that
	// varies between runs.
	Input string `json:"input,omitempty"`
	// Logs is the directory holding this run's per-call logs and its event
	// log.
	Logs string `json:"logs,omitempty"`
	// Plan marks a run made by `plan`: a rehearsal, journaled but not
	// resumable, not real work. It is kept so its trace can still be looked
	// at (`dot` and `flamegraph` on this run's id), but every record in it
	// is permanently ineligible to stand in for real work, in Restorable
	// and in Journal.LastSuccessful alike. A run that did nothing must
	// never leave state saying the work is done.
	Plan bool `json:"plan,omitempty"`
	// Tasks is what happened to each call in this run, keyed by path.
	Tasks    map[string]Record `json:"tasks,omitempty"`
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished,omitempty"`
	Summary  string            `json:"summary,omitempty"`
}

// Done reports whether the run reached an end of its own, rather than being
// interrupted.
func (r Run) Done() bool { return r.Status.Terminal() }

// Counts totals the statuses of the calls recorded in the run.
func (r Run) Counts() map[Status]int {
	out := map[Status]int{}
	for _, rec := range r.Tasks {
		out[rec.Status]++
	}
	return out
}

// Restorable returns the record of a call that a continued run may stand in
// for: it succeeded in that run, its value was not withheld as Secret or
// left Record.Partial, and its fingerprint has not changed since.
func (r Run) Restorable(path, fingerprint string) (Record, bool) {
	if r.Plan {
		return Record{}, false
	}
	rec, ok := r.Tasks[path]
	if !ok || rec.Partial || rec.Secret || !rec.worked() || rec.Fingerprint != fingerprint {
		return Record{}, false
	}
	return rec, true
}

// Journal is what previous runs left behind: the history, newest last.
type Journal struct {
	Version int       `json:"version"`
	Updated time.Time `json:"updated"`
	Runs    []Run     `json:"runs"`
}

// journalVersion is the layout written by this build.
const journalVersion = 1

// keepRuns is how many runs the journal remembers. Older ones are dropped as
// new ones arrive, so the file does not grow without bound.
const keepRuns = 20

// LastSuccessful returns the most recent record of a call that still
// applies: the last time it *succeeded*, with the fingerprint it was called
// with unchanged. It is what a freshness check ([work.LastRecord]) reads.
// [Journal.MostRecent], by contrast, ignores both status and fingerprint.
//
// A call whose configuration changed has no last successful record, so a
// freshness check built on this is stale by default rather than by accident.
func (j *Journal) LastSuccessful(path, fingerprint string) (Record, bool) {
	if j == nil {
		return Record{}, false
	}
	for i := len(j.Runs) - 1; i >= 0; i-- {
		if j.Runs[i].Plan {
			continue
		}
		rec, ok := j.Runs[i].Tasks[path]
		if !ok || !rec.worked() || rec.Fingerprint != fingerprint {
			continue
		}
		return rec, true
	}
	return Record{}, false
}

// Run returns the run with the given id.
func (j *Journal) Run(id string) (Run, bool) {
	if j == nil {
		return Run{}, false
	}
	for i := len(j.Runs) - 1; i >= 0; i-- {
		if j.Runs[i].ID == id {
			return j.Runs[i], true
		}
	}
	return Run{}, false
}

// Recent returns the last n runs, newest first.
func (j *Journal) Recent(n int) []Run {
	if j == nil {
		return nil
	}
	out := make([]Run, 0, n)
	for i := len(j.Runs) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, j.Runs[i])
	}
	return out
}

// Paths returns every call path the journal has heard of, sorted.
func (j *Journal) Paths() []string {
	if j == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, run := range j.Runs {
		for path := range run.Tasks {
			seen[path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// MostRecent returns the newest record for a path, whatever its status and
// fingerprint, along with the id of the run it came from. This is what
// `state` prints, as opposed to what a freshness check
// ([Journal.LastSuccessful]) may act on.
func (j *Journal) MostRecent(path string) (Record, string, bool) {
	if j == nil {
		return Record{}, "", false
	}
	for i := len(j.Runs) - 1; i >= 0; i-- {
		if rec, ok := j.Runs[i].Tasks[path]; ok {
			return rec, j.Runs[i].ID, true
		}
	}
	return Record{}, "", false
}

// save adds or replaces a run, keeping the history bounded.
func (j *Journal) save(run Run) {
	for i := range j.Runs {
		if j.Runs[i].ID == run.ID {
			j.Runs[i] = run
			return
		}
	}
	j.Runs = append(j.Runs, run)
	if len(j.Runs) > keepRuns {
		j.Runs = j.Runs[len(j.Runs)-keepRuns:]
	}
}

// Store is where runs are checkpointed. It is what a workflow's freshness
// checks read through, what `state` and `runs` print, and what makes an
// interrupted run something that can be continued rather than something
// that must be redone.
type Store interface {
	// Load returns the journal, which is empty rather than missing when
	// nothing has been recorded yet.
	Load(ctx context.Context) (*Journal, error)
	// Save writes a run as it stands. The runner calls it when a run
	// starts, after every call finishes, and when the run ends.
	Save(ctx context.Context, run Run) error
}

// NewFileStore returns a [Store] backed by the JSON file at path, rewritten
// atomically at every checkpoint so an interrupted run still leaves behind
// what it got done.
func NewFileStore(path string) Store { return &fileStore{path: path} }

type fileStore struct {
	path string

	mu sync.Mutex
	j  *Journal
}

// Load reads the journal, returning an empty one if the file does not exist.
func (s *fileStore) Load(context.Context) (*Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	return j, nil
}

func (s *fileStore) loadLocked() (*Journal, error) {
	if s.j != nil {
		return s.j, nil
	}
	data, err := os.ReadFile(s.path)
	switch {
	case os.IsNotExist(err):
		s.j = &Journal{Version: journalVersion}
		return s.j, nil
	case err != nil:
		return nil, fmt.Errorf("read state %s: %w", s.path, err)
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", s.path, err)
	}
	j.Version = journalVersion
	s.j = &j
	return s.j, nil
}

// Save implements [Store].
func (s *fileStore) Save(_ context.Context, run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.loadLocked()
	if err != nil {
		return err
	}
	j.save(run)
	j.Updated = time.Now()
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*")
	if err != nil {
		return err
	}
	// Best-effort: once the rename below succeeds there is nothing left to
	// remove, and this is what cleans up the temp file when it does not.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the write error is the one that matters
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// memoryStore is an in-memory [Store].
type memoryStore struct {
	mu sync.Mutex
	j  Journal
}

// NewMemoryStore returns a [Store] that keeps its journal in memory rather
// than on disk, for a test that wants a real Store without a filesystem.
func NewMemoryStore() Store { return &memoryStore{} }

// Load implements [Store].
func (s *memoryStore) Load(context.Context) (*Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Journal{Version: journalVersion, Updated: s.j.Updated}
	for _, run := range s.j.Runs {
		copied := run
		copied.Tasks = map[string]Record{}
		for k, v := range run.Tasks {
			copied.Tasks[k] = v
		}
		out.Runs = append(out.Runs, copied)
	}
	return &out, nil
}

// Save implements [Store].
func (s *memoryStore) Save(_ context.Context, run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := run
	copied.Tasks = map[string]Record{}
	for k, v := range run.Tasks {
		copied.Tasks[k] = v
	}
	s.j.save(copied)
	s.j.Updated = time.Now()
	return nil
}

// Duration serializes as "1m30s" rather than a nanosecond count, so a
// journal file reads the way a person would write it.
type Duration time.Duration

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).Round(time.Millisecond).String()), nil
}

func (d *Duration) UnmarshalText(b []byte) error {
	parsed, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
