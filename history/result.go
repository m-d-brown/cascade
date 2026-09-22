package history

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// TaskResult is one call's outcome.
type TaskResult struct {
	Path     string
	Name     string
	Parent   string
	Doc      string
	Tag      string
	Status   Status
	Summary  string
	Err      error
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	LogPath  string
}

// Result is the outcome of a whole run.
type Result struct {
	// ID names the run, and its entry in the journal and log directory.
	ID       string
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	// Order lists every call's path in the order it finished. Since a caller
	// always finishes after everything it called, this reads as callees
	// before their caller, and the root call last of all.
	Order  []string
	Tasks  map[string]*TaskResult
	Counts map[Status]int
	// Aborted is set when the run stopped early: a critical call failed, a
	// call returned a Fatal error, StopOnError tripped, or the context was
	// canceled.
	Aborted bool
	// AbortReason explains why.
	AbortReason string
}

// Failed returns the failed calls, in the order they started.
func (r *Result) Failed() []*TaskResult {
	var out []*TaskResult
	for _, path := range r.Order {
		if t := r.Tasks[path]; t != nil && t.Status == Failed {
			out = append(out, t)
		}
	}
	return out
}

// Err returns a single error summarizing the failures, or nil.
func (r *Result) Err() error {
	var errs []error
	for _, t := range r.Failed() {
		errs = append(errs, fmt.Errorf("%s: %w", t.Path, t.Err))
	}
	if r.Aborted && len(errs) == 0 {
		errs = append(errs, errors.New(r.AbortReason))
	}
	return errors.Join(errs...)
}

// ExitCode is 1 if anything failed or the run was aborted, else 0.
func (r *Result) ExitCode() int {
	if r.Aborted {
		return 1
	}
	for _, t := range r.Tasks {
		if t.Status == Failed {
			return 1
		}
	}
	return 0
}

// SortedResults returns every call's result in Result.Order: callees before
// their caller, root last.
func (r *Result) SortedResults() []*TaskResult {
	out := make([]*TaskResult, 0, len(r.Tasks))
	index := map[string]int{}
	for i, path := range r.Order {
		index[path] = i
	}
	for _, t := range r.Tasks {
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return index[out[i].Path] < index[out[j].Path] })
	return out
}
