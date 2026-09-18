// Package work is a small engine for pipeline-oriented work, written as
// ordinary Go control flow rather than a graph you declare up front.
//
// There is no builder and nothing to collect: a workflow is a plain function
// that calls other plain functions through [Do] or [Go], the way any Go
// program calls other Go programs' worth of code. What runs, in what order,
// and how much of it runs concurrently is exactly what the function's own
// if-statements, loops and goroutines say — because that is what it is.
//
//	func release(ctx *work.Context, version string) (string, error) {
//	    linux := work.Go(ctx, "build-linux", func(ctx *work.Context) (string, error) {
//	        return build(ctx, "linux", version)
//	    })
//	    darwin := work.Go(ctx, "build-darwin", func(ctx *work.Context) (string, error) {
//	        return build(ctx, "darwin", version)
//	    })
//	    a, err := linux.Get()
//	    if err != nil {
//	        return "", err
//	    }
//	    b, err := darwin.Get()
//	    if err != nil {
//	        return "", err
//	    }
//	    return publish(ctx, a, b)
//	}
//
// [Do] calls a unit of work and waits for it; [Go] starts one and hands back
// a [Future] to wait on later, which is the whole of how two things run at
// the same time. Every call is named, and the name is where the framework's
// half of the work happens: the name, plus the names of the calls it is
// nested inside, is a path — "release/build-linux" — and that path is the
// key for everything downstream of the call itself. Log lines are filed
// under it, the live display draws it as a row in a tree, [history.Dot] draws
// it as a box, and the state journal remembers what it produced there, so
// that [Options.Continue] can hand the same value back instead of calling
// the function again.
//
// That is not a hypothetical list — it is what naming one call actually
// produces, from a real run of this module's own examples/complete (a few
// fields trimmed to fit a line; nothing here is staged). The plain-mode
// line for "build-linux":
//
//	05:46:45 release/build-linux      running
//	05:46:47 release/build-linux      ✓ dist/widget-1.4.0-linux (14.2 MiB) (1.85s)
//
// its line in the run's event log, one JSON object per event:
//
//	{"event":"task-finished","task":"release/build-linux","status":"ok","message":"dist/widget-1.4.0-linux (14.2 MiB)"}
//
// its box in [history.Dot]'s output:
//
//	n8[...label="build-linux  [ok]"...];
//
// and what `state` still says about it after the run has long since ended:
//
//	release/build-linux    ok    2026-08-30 05:46    dist/widget-1.4.0-linux (14.2 MiB)
//
// See the README for the same call's line in its own log file and its span
// on a flamegraph too. None of it took a second line of code, a registered
// span, or a metrics call — the string given to [Go] is the only thing
// every one of those views has to agree on, and there is only one string to
// give.
//
// A [Context] is what a call is handed while it runs: identity and logging.
// It is also a [context.Context] in its own right, so cancellation, a
// deadline, and ^C all reach the work through the one parameter it already
// takes. Nothing here routes an effect anywhere — a call that wants to
// touch the outside world through something interceptable reaches for
// [github.com/mdbrown/cascade/world] explicitly, handing it the same
// [Context] it already has.
//
// See docs/architecture.md and docs/design.md in this module for the shape
// of the whole framework and why it is built this way.
package work
