// Package work provides the core workflow execution engine for cascade.
//
// In cascade, workflows are written as standard Go functions rather than
// declared dependency graphs. You coordinate tasks directly using [Do] for
// synchronous calls and [Go] for concurrent execution, controlling flow with
// standard Go if-statements, loops, and error handling.
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
// [Do] executes a task synchronously and waits for its result. [Go] starts a
// task in a new goroutine and returns a [Future] that resolves upon calling
// [Future.Get].
//
// Each task is identified by a hierarchical path derived from its name and
// enclosing parent tasks (for example, "release/build-linux"). This path
// serves as the uniform key across all runtime features:
//
//   - Terminal output: Displayed as an active row in the live tree.
//   - Logs: Lines are associated with the task path in flow.log.
//   - Events: Structured JSON events record status transitions (e.g. task-finished).
//   - Visualization: Nodes in [history.Dot] and spans in [history.Flamegraph].
//   - Caching: Persisted in the state journal, allowing [Options.Continue]
//     to restore results in subsequent runs.
//
// # Context and External Side Effects
//
// Each task receives a [*Context], which wraps [context.Context] to provide
// cancellation, deadline handling, and scoped logging ([Context.Logf],
// [Context.Statusf], [Context.Summarize]).
//
// To intercept external side effects for dry-run simulation or interactive
// user confirmation, see [github.com/m-d-brown/cascade/world].
//
// For in-depth architectural notes and usage guides, see docs/guide.md and
// docs/design.md in the repository root.
package work
