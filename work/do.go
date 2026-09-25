package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/m-d-brown/cascade/history"
)

// taskConfig is what [Option]s configure for one call to [Do] or [Go].
type taskConfig struct {
	secret      bool
	critical    bool
	timeout     time.Duration
	fingerprint string
	tag         string
	doc         string
	// neverRestore is set only on the run's own root call: continuing a run
	// means re-entering the workflow function so it can walk through and
	// retry whatever did not finish, so the root call itself must never be
	// handed back as a single resumed unit. Only the nested calls it goes
	// on to make are ever eligible for that.
	neverRestore bool
}

// Option configures one call to [Do] or [Go].
type Option func(*taskConfig)

// Secret marks a call's return value as one that must never be written to
// the state journal. Such a call therefore has no last run to consult
// through [LastRecord], and is never handed back by [Options.Continue]: it
// always runs again.
func Secret() Option { return func(c *taskConfig) { c.secret = true } }

// Critical marks a call whose failure aborts the whole run, canceling every
// other call still in flight, instead of only stopping the branch of the
// workflow that called it.
func Critical() Option { return func(c *taskConfig) { c.critical = true } }

// Timeout bounds how long a call may run. Past it the call's context is
// canceled and it fails, the same as any other failure. Its caller decides
// what that means; everything else in the run is left alone.
//
// The deadline arrives through the [Context], so code using it as a context
// (exec.CommandContext(ctx, …), a select on ctx.Done()) stops on its own.
// Code that ignores the context does not.
func Timeout(d time.Duration) Option { return func(c *taskConfig) { c.timeout = d } }

// Config records the configuration a call depends on. It is mixed into the
// call's fingerprint, which [Options.Continue] and [LastRecord] compare
// against a recorded run. Changing it means there is no result to return
// for this call, so it runs fresh instead of reusing a result recorded
// under a configuration it no longer matches.
func Config(fingerprint string) Option { return func(c *taskConfig) { c.fingerprint = fingerprint } }

// Tag labels a call for [history.Dot] and the trace it draws from: calls
// that share a tag are drawn as one cluster. It has no effect on what runs;
// it is a name for referring to calls from outside, not a dependency. The
// last Tag given to a call wins.
func Tag(name string) Option { return func(c *taskConfig) { c.tag = name } }

// Doc describes a call in one line, shown under its name by [history.Dot] and in
// `state`. It has no effect on what runs.
func Doc(doc string) Option { return func(c *taskConfig) { c.doc = doc } }

func buildConfig(opts []Option) taskConfig {
	var c taskConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// taskOutcome is the untyped result of one call: the plumbing [Do] and [Go]
// share.
type taskOutcome struct {
	value   any
	skipped bool
	err     error
}

// Do calls fn as one named unit of work and waits for it. Calling it
// through Do is the same as calling fn directly, except the call is
// tracked: logged under its own path, drawn as a box by [history.Dot], and,
// if it succeeds, recorded so that [Options.Continue] can return its result
// instead of calling fn again.
//
// fn is an ordinary function: whatever it returns is what the caller gets,
// and an error fails the call exactly the way it would fail anything else in
// Go. One that needs nothing but ctx can be named directly, with nothing
// wrapping it:
//
//	func fetchSource(ctx *work.Context, version string) (string, error) { … }
//
//	func report(ctx *work.Context) (string, error) {
//	    at, err := work.Do(ctx, "snapshot", snapshot)
//	    if err != nil {
//	        return "", err
//	    }
//	    ctx.Summarize("backup is at %s", at)
//	    return at, nil
//	}
//
//	_, err := work.Do(ctx, "report", report)
//
// One that needs more than ctx is bound to its extra arguments with a
// closure at the call site, not at its own declaration:
//
//	work.Do(ctx, "source", func(ctx *work.Context) (string, error) {
//	    return fetchSource(ctx, version)
//	})
//
// See [Context.Summarize] for the one-line summary the run shows for the
// call. Return [Skip] as fn's error to report work the call decided not to
// do (a disabled feature, a case that does not apply). Its reason becomes
// the summary, and it is reported apart from an ordinary success without
// failing the run.
//
// Do is a free function rather than a method on [Context] because it needs
// a type parameter for what fn returns, which [Context] itself knows
// nothing about.
func Do[T any](ctx *Context, name string, fn func(ctx *Context) (T, error), opts ...Option) (T, error) {
	cfg := buildConfig(opts)
	path := ctx.runner.childPath(ctx.Path(), name)
	v, err := ctx.runner.call(ctx, path, name, cfg, func(c *Context) taskOutcome {
		return callTyped(c, fn)
	})
	return decodeAs[T](v), err
}

// Future is a call started with [Go]: work already under way, whose result
// is read with [Future.Get].
type Future[T any] struct {
	done  chan struct{}
	ctx   *Context
	value T
	err   error
}

// Go starts fn as a named unit of work running concurrently with its caller,
// and returns immediately with a [Future] for its result. This is the
// entire mechanism for running two things at once:
//
//	linux := work.Go(ctx, "build-linux", func(ctx *work.Context) (string, error) {
//	    return build(ctx, "linux", version)
//	})
//	darwin := work.Go(ctx, "build-darwin", func(ctx *work.Context) (string, error) {
//	    return build(ctx, "darwin", version)
//	})
//	a, err := linux.Get()
//	…
//	b, err := darwin.Get()
//
// fn follows the same shape as [Do]'s. [Options.Concurrency] bounds how many
// calls started this way run at once; a call whose turn has not come waits
// for a slot before fn starts.
func Go[T any](ctx *Context, name string, fn func(ctx *Context) (T, error), opts ...Option) *Future[T] {
	cfg := buildConfig(opts)
	path := ctx.runner.childPath(ctx.Path(), name)
	r := ctx.runner
	f := &Future[T]{done: make(chan struct{}), ctx: ctx}
	r.calls.Add(1)
	go func() {
		defer r.calls.Done()
		defer close(f.done)
		release, ok := r.acquire(ctx.context())
		if !ok {
			_, f.err = r.call(ctx, path, name, cfg, func(c *Context) taskOutcome {
				return taskOutcome{err: c.Err()}
			})
			return
		}
		defer release()
		var v any
		v, f.err = r.call(ctx, path, name, cfg, func(c *Context) taskOutcome {
			return callTyped(c, fn)
		})
		f.value = decodeAs[T](v)
	}()
	return f
}

// Get waits for the call to finish and returns what it produced. It also
// returns early if the run is canceled, through the [Context] the call was
// started on, which is how a caller waiting on several futures still
// notices the run going down.
func (f *Future[T]) Get() (T, error) {
	// A call that has finished returns what it produced, even when the run
	// is being canceled at the same moment.
	select {
	case <-f.done:
		return f.value, f.err
	default:
	}
	select {
	case <-f.done:
		return f.value, f.err
	case <-f.ctx.Done():
		var zero T
		return zero, f.ctx.Err()
	}
}

// callTyped adapts a typed [Do]/[Go] function to the untyped core. A skip's
// reason becomes the call's summary the same way [Context.Summarize] would
// set it. Skip is how a call reports what it decided instead of doing the
// work, and that decision is what's worth showing.
func callTyped[T any](c *Context, fn func(*Context) (T, error)) taskOutcome {
	v, err := fn(c)
	var sk skipped
	if errors.As(err, &sk) {
		c.Summarize("%s", sk.reason)
		return taskOutcome{value: v, skipped: true}
	}
	if err != nil {
		return taskOutcome{value: v, err: err}
	}
	return taskOutcome{value: v}
}

// call resolves one call at path: return a recorded result if the run
// being continued already made it, otherwise run fn and record what
// happened. It is the core [Do] and [Go] both build on.
//
// Every branch emits TaskStarted before anything else. A log line or a
// TaskFinished for a row that was never announced has nothing to attach to,
// so an observer building the tree live is guaranteed to see a call's start
// before its end, whichever path it takes to get there.
func (r *Runner) call(parent *Context, path, name string, cfg taskConfig, fn func(*Context) taskOutcome) (any, error) {
	parentPath := parentOf(path)

	if parent.context().Err() != nil {
		r.emit(history.Event{Kind: history.TaskStarted, Path: path, Parent: parentPath, Doc: cfg.doc, Tag: cfg.tag, Status: history.Canceled, Time: time.Now()})
		tr := &history.TaskResult{Path: path, Name: name, Parent: parentPath, Doc: cfg.doc, Tag: cfg.tag, Status: history.Canceled, Summary: "run aborted"}
		r.settle(tr, nil)
		return nil, parent.context().Err()
	}

	fp := r.fingerprint(cfg)
	if rec, ok := r.restorable(path, fp); ok && !cfg.neverRestore {
		r.emit(history.Event{Kind: history.TaskStarted, Path: path, Parent: parentPath, Doc: cfg.doc, Tag: cfg.tag, Status: history.Resumed, Time: rec.Started})
		child := r.newContext(parent, path, name, cfg)
		child.Logf("restored from run %s: %s", r.continuing.ID, rec.Summary)
		tr := &history.TaskResult{
			Path: path, Name: name, Parent: parentPath, Doc: cfg.doc, Tag: cfg.tag, Status: history.Resumed,
			Summary: rec.Summary, Started: rec.Started, Finished: rec.Finished,
			Duration: time.Duration(rec.Duration), LogPath: child.logPath,
		}
		r.settle(tr, &rec)
		carried := rec
		carried.Status, carried.Doc, carried.Tag = history.Resumed, cfg.doc, cfg.tag
		r.recordTask(path, carried)
		return rec.Value, nil
	}

	child := r.newContext(parent, path, name, cfg)
	child.started = time.Now()
	if cfg.timeout > 0 {
		timed, cancel := context.WithTimeout(child.ctx, cfg.timeout)
		defer cancel()
		child.ctx = timed
	}
	r.emit(history.Event{Kind: history.TaskStarted, Path: path, Parent: parentPath, Doc: cfg.doc, Tag: cfg.tag, Status: history.Running, Time: child.started})

	out := safeCall(fn, child)
	if cfg.timeout > 0 && errors.Is(child.Err(), context.DeadlineExceeded) && parent.context().Err() == nil {
		out.err = fmt.Errorf("timed out after %s", cfg.timeout)
	}

	tr := &history.TaskResult{Path: path, Name: name, Parent: parentPath, Doc: cfg.doc, Tag: cfg.tag, Started: child.started, LogPath: child.logPath}
	switch {
	case out.err != nil:
		tr.Status, tr.Err, tr.Summary = history.Failed, out.err, firstLine(out.err.Error())
		child.Warnf("failed: %v", out.err)
	case out.skipped:
		tr.Status, tr.Summary = history.Skipped, firstNonEmpty(child.getSummary(), "skipped")
	default:
		tr.Status, tr.Summary = history.Succeeded, firstNonEmpty(child.getSummary(), defaultSummary(out.value))
	}
	r.settle(tr, nil)
	r.recordLive(tr, out, cfg)

	if reason, stop := r.abortReason(cfg, tr); stop {
		r.abort(reason)
	}
	return out.value, out.err
}

// newContext builds the context handed to one call.
func (r *Runner) newContext(parent *Context, path, name string, cfg taskConfig) *Context {
	child := &Context{
		path: path, name: name, logPath: r.opts.Logs.Combined(),
		ctx: parent.ctx, runner: r,
	}
	child.sink = func(level slog.Level, kind history.EventKind, msg string) {
		now := time.Now()
		r.opts.Logs.WriteCombined(path, level, msg, now)
		r.emit(history.Event{Kind: kind, Path: path, Parent: parentOf(path), Level: level, Message: msg, Time: now})
	}
	return child
}

// safeCall runs fn and turns a panic into an ordinary failure, so one call
// panicking does not take the whole run down.
func safeCall(fn func(*Context) taskOutcome, ctx *Context) (out taskOutcome) {
	defer func() {
		if p := recover(); p != nil {
			ctx.Warnf("panic: %v", p)
			ctx.Logf("%s", debug.Stack())
			out = taskOutcome{err: fmt.Errorf("panic: %v", p)}
		}
	}()
	return fn(ctx)
}

// settle records a call's terminal result in the runner's own bookkeeping
// and tells the observers. Every path a call can end on, live or resumed,
// runs through here. resumed is the record it stood in for, when there is
// one: its Started/Finished/Duration are what the journal remembers, not
// how long standing in for it took (which is no time at all).
func (r *Runner) settle(tr *history.TaskResult, resumed *history.Record) {
	if tr.Finished.IsZero() {
		tr.Finished = time.Now()
	}
	if !tr.Started.IsZero() && resumed == nil {
		tr.Duration = tr.Finished.Sub(tr.Started)
	}
	r.mu.Lock()
	r.tasks[tr.Path] = tr
	r.order = append(r.order, tr.Path)
	r.mu.Unlock()
	r.emit(history.Event{Kind: history.TaskFinished, Path: tr.Path, Parent: tr.Parent, Status: tr.Status, Result: tr, Time: tr.Finished})
}

// recordLive writes a call that actually ran this run to the journal.
func (r *Runner) recordLive(tr *history.TaskResult, out taskOutcome, cfg taskConfig) {
	rec := history.Record{
		Path: tr.Path, Doc: cfg.doc, Tag: cfg.tag, Fingerprint: r.fingerprint(cfg), Status: tr.Status,
		Summary: tr.Summary, Started: tr.Started, Finished: tr.Finished,
		Duration: history.Duration(tr.Duration), Secret: cfg.secret,
	}
	if tr.Err != nil {
		rec.Error = tr.Err.Error()
	}
	switch {
	case cfg.secret:
		// Never written, whatever it produced: a call marked Secret has no
		// last run to consult, and always runs again.
	case !journalSafe(out.value):
		rec.Partial = true
		r.emit(history.Event{Kind: history.TaskLog, Path: tr.Path, Level: slog.LevelWarn,
			Message: fmt.Sprintf("not recording this call's result: a %T is not something the state journal can carry, "+
				"so it cannot be stood in for later", out.value)})
	default:
		rec.Value, rec.HasValue = out.value, true
	}
	r.recordTask(tr.Path, rec)
}

// recordTask adds one call's record to the run being checkpointed and
// writes it to the store: the run's own account of what it did, updated as
// it happens rather than assembled at the end.
func (r *Runner) recordTask(path string, rec history.Record) {
	if r.opts.Store == nil {
		return
	}
	r.mu.Lock()
	r.run.Tasks[path] = rec
	r.mu.Unlock()
	r.checkpoint(context.Background())
}

// restorable reports what the run being continued already did for path.
func (r *Runner) restorable(path, fingerprint string) (history.Record, bool) {
	if r.continuing.ID == "" {
		return history.Record{}, false
	}
	return r.continuing.Restorable(path, fingerprint)
}

func (r *Runner) abortReason(cfg taskConfig, tr *history.TaskResult) (string, bool) {
	if tr.Status != history.Failed {
		return "", false
	}
	switch {
	case isFatal(tr.Err):
		return fmt.Sprintf("%s returned a fatal error: %v", tr.Path, tr.Err), true
	case cfg.critical:
		return fmt.Sprintf("critical call %s failed: %v", tr.Path, tr.Err), true
	case r.opts.StopOnError:
		return fmt.Sprintf("%s failed and --stop-on-error is set: %v", tr.Path, tr.Err), true
	}
	return "", false
}

// RecordMeta is what the journal remembers about a call besides its value:
// when it last ran, how it ended, and what it said.
type RecordMeta struct {
	Status   history.Status
	Summary  string
	Started  time.Time
	Finished time.Time
	// HasValue reports whether the record carries a value at all. It is
	// false for a call made with [Secret], or one JSON could not carry.
	HasValue bool
}

// Age is how long ago the call last ran.
func (m RecordMeta) Age() time.Duration { return time.Since(m.Finished) }

// LastRecord returns what a previous run of this workflow (this one being
// continued, an earlier one, or the one before that) recorded the last time
// name was called from the same place ctx is now, if the call succeeded and
// its fingerprint still matches.
//
// It takes the same options [Do] would, so the check reads as asking about
// the call that would otherwise be made: pass the [Config] the call depends
// on, and a record made under a different one is not offered. Options that
// do not bear on the record, such as [Timeout] and [Critical], are accepted
// and ignored.
//
// It is how a call decides whether it needs to run at all, written as an
// ordinary if statement rather than a check the framework asks on its
// behalf:
//
//	if v, meta, ok := work.LastRecord[string](ctx, "snapshot", work.Config(cfg)); ok && meta.Age() < every {
//	    return v, nil
//	}
//	return work.Do(ctx, "snapshot", takeSnapshot, work.Config(cfg))
//
// Because that decision runs before anything else is called, nothing behind
// it (the archive this snapshot would have needed, the files that archive
// would have collected) is ever called either. That is what "do this at
// most once a day" means here: the code simply does not reach the rest of
// the workflow.
//
// A value that does not fit T at all, for example if this call was rewired
// to do something different from what it once did, panics naming both
// types: the run-time equivalent of a compile error Go can't catch across
// this boundary.
func LastRecord[T any](ctx *Context, name string, opts ...Option) (value T, meta RecordMeta, ok bool) {
	if ctx == nil || ctx.runner == nil || ctx.runner.journal == nil {
		return value, meta, false
	}
	path := ctx.Path()
	if path == "" {
		path = name
	} else {
		path = path + "/" + name
	}
	fp := ctx.runner.fingerprint(buildConfig(opts))
	rec, ok := ctx.runner.journal.LastSuccessful(path, fp)
	if !ok {
		return value, meta, false
	}
	meta = RecordMeta{Status: rec.Status, Summary: rec.Summary, Started: rec.Started, Finished: rec.Finished, HasValue: rec.HasValue}
	if rec.HasValue {
		value = decodeAs[T](rec.Value)
	}
	return value, meta, true
}

// defaultSummary is what a call shows when it never calls [Context.Summarize]:
// its own return value, if that reads as a line on its own, or "done"
// otherwise. A call producing a barrier `bool`, a count, or a struct has
// nothing more specific to report than that it finished.
func defaultSummary(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "done"
}

// decodeAs converts a recorded value into T. The common case is a live run,
// where v already has its exact Go type because nothing has restored it from
// anywhere; a value carried over from the journal has been through JSON, so
// it is re-decoded into T rather than asserted, the same way an int comes
// back a float64 on the wire.
func decodeAs[T any](v any) T {
	var zero T
	if v == nil {
		return zero
	}
	if t, ok := v.(T); ok {
		return t
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("flow: a recorded value could not be checked against its declared type: %v", err))
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		panic(fmt.Sprintf("flow: a recorded %T does not fit the %T the call declared: %v", v, out, err))
	}
	return out
}
