package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/m-d-brown/cascade/history"
)

// Options configures a run.
type Options struct {
	// Name is the run's own top-level call: everything the workflow does
	// nests under a call of this name. Empty defaults to "run".
	Name string
	// Concurrency caps how many calls started with [Go] run at once. Zero
	// means unbounded. A synchronous [Do] call never waits on this: it
	// runs in its caller's own goroutine, which was already counted (or not
	// subject to the cap at all) when that goroutine started.
	Concurrency int
	// Plan marks the run itself as one that changed nothing, so its trace
	// is still journaled (for `dot` and `flamegraph`) but permanently
	// excluded from [history.Run.Restorable] and [history.Journal.LastSuccessful]:
	// a run that did nothing must never leave state saying the work is
	// done. Set this whenever the workflow's own effects are routed through
	// a dry-run [github.com/m-d-brown/cascade/world.World].
	Plan bool
	// StopOnError aborts the run on the first failure. By default a
	// failure only stops the branch of the workflow that returned it, and
	// the rest keeps going.
	StopOnError bool
	// Store is the state journal: what [LastRecord] reads, and where the
	// run is checkpointed as it goes. Nil means the run neither reads nor
	// writes recorded state.
	Store history.Store
	// RunID names this run and its entry in the journal. Empty takes the
	// name of the log directory, or the time the run started.
	RunID string
	// Input is a one-line description of this run's own input, recorded as
	// [history.Run.Input]. Empty means the workflow declared none.
	Input string
	// Continue names a run in the journal to carry on from: a call that
	// succeeded in that run, and has not changed since, is handed back
	// rather than made again. It is how an interrupted run is finished
	// rather than redone.
	Continue string
	// Observer receives run events: a terminal UI, a plain logger, or both.
	Observer history.Observer
	// Logs is where a run's per-call log files and combined log are written.
	// Nil means the run writes no log files.
	Logs *history.LogDir
}

// Runner executes a workflow: a plain function that calls other plain
// functions through [Do] and [Go].
//
// There is nothing to walk ahead of time, because there is nothing but the
// function itself: no declared graph, no upfront list of what will run.
// What happens is exactly what the root call's own code does, discovered as
// it does it.
type Runner struct {
	root func(ctx *Context) (string, error)
	opts Options
	id   string

	binaryFP   string
	journal    *history.Journal
	continuing history.Run
	sem        chan struct{}

	emitMu sync.Mutex
	// calls counts the calls started with [Go] that have not finished. A
	// run ends only once they all have, whether or not anything read their
	// futures.
	calls sync.WaitGroup

	mu       sync.Mutex
	tasks    map[string]*history.TaskResult
	order    []string
	names    map[string]map[string]int
	run      history.Run
	aborted  bool
	abortWhy string
	cancel   context.CancelFunc
}

// NewRunner returns a runner for root, the workflow's top-level call. root
// takes exactly the shape [Do] itself takes, since it is not otherwise
// different from any other named call: the run's own summary line comes
// from the same place a nested call's would, [Context.Summarize] or root's
// own return value.
func NewRunner(root func(ctx *Context) (string, error), opts Options) *Runner {
	if opts.Name == "" {
		opts.Name = "run"
	}
	return &Runner{root: root, opts: opts}
}

// Run executes the workflow and returns its result. The error is non-nil
// only for a problem with the run itself (an unreadable journal); a failure
// of the workflow's own code is reported in the [history.Result].
func (r *Runner) Run(ctx context.Context) (*history.Result, error) {
	r.binaryFP = binaryFingerprint()
	if r.opts.Store != nil {
		var err error
		if r.journal, err = r.opts.Store.Load(ctx); err != nil {
			return nil, err
		}
	}
	if r.opts.Continue != "" {
		if r.journal == nil {
			return nil, fmt.Errorf("cannot continue run %q: this run keeps no state", r.opts.Continue)
		}
		prev, ok := r.journal.Run(r.opts.Continue)
		if !ok {
			return nil, fmt.Errorf("no run %q in the journal", r.opts.Continue)
		}
		r.continuing = prev
	}

	r.tasks = map[string]*history.TaskResult{}
	r.names = map[string]map[string]int{}

	limit := r.opts.Concurrency
	if limit <= 0 {
		limit = 1 << 20 // effectively unbounded
	}
	r.sem = make(chan struct{}, limit)

	r.id = r.runID()
	started := time.Now()
	r.run = history.Run{
		ID:      r.id,
		Status:  history.Running,
		Input:   r.opts.Input,
		Plan:    r.opts.Plan,
		Tasks:   map[string]history.Record{},
		Started: started,
	}
	if r.opts.Logs != nil {
		r.run.Logs = r.opts.Logs.Dir
	}
	if r.continuing.ID != "" {
		r.run.Summary = "continuing run " + r.continuing.ID
	}
	// Checkpoint before anything happens, so a run interrupted in its first
	// second is still a run somebody can ask about.
	r.checkpoint(ctx)
	r.emit(history.Event{Kind: history.RunStarted, Time: started, Name: r.opts.Name, LogPath: r.opts.Logs.Combined()})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.cancel = cancel

	root := &Context{ctx: runCtx, runner: r}
	rootPath := r.childPath(root.Path(), r.opts.Name)
	_, rootErr := r.call(root, rootPath, r.opts.Name, taskConfig{neverRestore: true}, func(c *Context) taskOutcome {
		return callTyped(c, r.root)
	})
	// The root can return with calls it started still running: one that
	// returns the first error among several futures leaves the rest going.
	// They are part of the run, so it waits for them rather than finishing
	// without their results.
	r.calls.Wait()

	if ctx.Err() != nil {
		r.abort("run canceled: " + ctx.Err().Error())
	}

	result := &history.Result{ID: r.id, Started: started, Counts: map[history.Status]int{}}
	r.mu.Lock()
	result.Order = append([]string(nil), r.order...)
	result.Tasks = make(map[string]*history.TaskResult, len(r.tasks))
	for k, v := range r.tasks {
		result.Tasks[k] = v
	}
	result.Aborted, result.AbortReason = r.aborted, r.abortWhy
	r.mu.Unlock()
	for _, t := range result.Tasks {
		result.Counts[t.Status]++
	}
	result.Finished = time.Now()
	result.Duration = result.Finished.Sub(result.Started)

	r.mu.Lock()
	r.run.Finished = result.Finished
	r.run.Status = history.Succeeded
	switch {
	case result.Aborted:
		r.run.Status, r.run.Summary = history.Canceled, result.AbortReason
	case result.ExitCode() != 0, rootErr != nil:
		r.run.Status, r.run.Summary = history.Failed, "the run failed"
	}
	r.mu.Unlock()
	// The run is over however it ended, so this checkpoint must be written
	// even when what ended it was the context being canceled.
	r.checkpoint(context.WithoutCancel(ctx))

	r.emit(history.Event{Kind: history.RunFinished, Time: result.Finished, Outcome: result})
	return result, nil
}

// runID is what this run is called: what the caller asked for, the name of
// its log directory, or the time it started.
func (r *Runner) runID() string {
	switch {
	case r.opts.RunID != "":
		return r.opts.RunID
	case r.opts.Logs != nil && r.opts.Logs.Dir != "":
		return r.opts.Logs.Run()
	}
	stamp := time.Now().Format(runIDLayout)
	id := stamp
	for n := 2; ; n++ {
		if _, taken := r.journal.Run(id); !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", stamp, n)
	}
}

// runIDLayout is how a run with no name of its own is named.
const runIDLayout = "2006-01-02T15-04-05"

// childPath allocates the path for one call under parent, disambiguating a
// name repeated under the same parent (a loop calling [Do] or [Go] with the
// same base name more than once) by suffixing it, so two calls never
// collide on one path. Giving each call its own name, the way a loop
// building several builds names each after its platform, means this rarely
// fires at all.
func (r *Runner) childPath(parent, name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[parent] == nil {
		r.names[parent] = map[string]int{}
	}
	n := r.names[parent][name]
	r.names[parent][name] = n + 1
	if n > 0 {
		name = fmt.Sprintf("%s#%d", name, n+1)
	}
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func parentOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

// fingerprint mixes a call's own configuration with the running binary, so a
// call is never told its earlier result still applies when the code that
// produced it no longer exists.
func (r *Runner) fingerprint(cfg taskConfig) string {
	if cfg.fingerprint == "" {
		return r.binaryFP
	}
	h := sha256.Sum256([]byte(r.binaryFP + "|" + cfg.fingerprint))
	return hex.EncodeToString(h[:])[:16]
}

// binaryFingerprint identifies the running program, so a call is not told
// its work is done by code that no longer exists.
func binaryFingerprint() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "unknown"
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())))
	return hex.EncodeToString(h[:])[:16]
}

// acquire takes one of the run's concurrency slots, for a call started with
// [Go].
func (r *Runner) acquire(ctx context.Context) (release func(), ok bool) {
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }, true
	case <-ctx.Done():
		return func() {}, false
	}
}

// abort records why the run is stopping and cancels everything still in
// flight.
func (r *Runner) abort(reason string) {
	r.mu.Lock()
	if !r.aborted {
		r.aborted, r.abortWhy = true, reason
	}
	r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) emit(e history.Event) {
	if r.opts.Observer == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Run == "" {
		e.Run = r.id
	}
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.opts.Observer.Handle(e)
}

// checkpoint writes the run as it stands. Every call that reaches a
// terminal status is in it, so an interrupted run leaves behind exactly what
// it got done. A failure to write one is reported and not fatal, because
// losing the record of work is not a reason to stop doing it.
func (r *Runner) checkpoint(ctx context.Context) {
	if r.opts.Store == nil {
		return
	}
	r.mu.Lock()
	snapshot := r.run
	snapshot.Tasks = make(map[string]history.Record, len(r.run.Tasks))
	for k, v := range r.run.Tasks {
		snapshot.Tasks[k] = v
	}
	r.mu.Unlock()
	if err := r.opts.Store.Save(ctx, snapshot); err != nil {
		r.emit(history.Event{Kind: history.RunLog, Level: slog.LevelWarn,
			Message: "could not checkpoint the run: " + err.Error()})
	}
}
