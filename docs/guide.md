# The Engine Guide

This guide covers how to write and run workflows directly in Go using the packages underlying Cascade. Use this Go API when you need dynamic control flow, such as conditional branches, loops, or runtime fan-out, that cannot be expressed in static YAML. (For YAML pipelines, see the [Pipeline Specification](pipeline-spec.md).)

## Contents

- [Calling Work: Do and Go](#calling-work-do-and-go)
- [Naming and Hierarchical Paths](#naming-and-hierarchical-paths)
- [Managing Concurrency](#managing-concurrency)
- [Task Results and Completion](#task-results-and-completion)
- [The Context](#the-context)
- [Structuring a Workflow](#structuring-a-workflow)
- [Execution and Error Handling](#execution-and-error-handling)
- [Caching and Skipping Work](#caching-and-skipping-work)
- [Side Effects and Dry-Run (The World)](#side-effects-and-dry-run-the-world)
- [Run Persistence and Resumption](#run-persistence-and-resumption)
- [Terminal Displays and Monitoring](#terminal-displays-and-monitoring)
- [Visualizing Traces (DOT & Flamegraphs)](#visualizing-traces-dot--flamegraphs)
- [The CLI Framework](#the-cli-framework)
- [Batching Operations](#batching-operations)
- [Running External Commands](#running-external-commands)
- [Operational Considerations and Gotchas](#operational-considerations-and-gotchas)

For complete code examples, see:

- [`examples/engine/complete`](../examples/engine/complete): Full demonstration of all engine capabilities.
- [`examples/engine/minimal`](../examples/engine/minimal): Minimal workflow demonstrating caching and skipping.

---

## Calling Work: Do and Go

Workflows organize execution around named function calls using `work.Do` and `work.Go`.

```go
func testUnit(ctx *work.Context, binary string) (int, error) {
    ctx.Logf("testing %s", binary)

    if err := runTests(ctx); err != nil {
        return 0, err
    }
    ctx.Summarize("312 tests passed")
    return 312, nil
}

// Invoke synchronously:
count, err := work.Do(ctx, "test-unit", func(ctx *work.Context) (int, error) {
    return testUnit(ctx, binary)
})
```

### `work.Do` Signature

```go
func Do[T any](ctx *Context, name string, fn func(ctx *Context) (T, error), opts ...Option) (T, error)
```

- `name`: Identifies the task. Forms the final segment of the task's hierarchical path.
- `fn`: The implementation function. Must return `(T, error)`. If `fn` does not require additional arguments, you can pass a matching function directly without an enclosing closure.
- `opts`: Optional configuration modifying execution behavior.

### Task Options

| Option             | Effect                                                                                                                                     |
| :----------------- | :----------------------------------------------------------------------------------------------------------------------------------------- |
| `work.Tag(name)`   | Assigns a group tag used for clustering in Graphviz DOT diagrams. Does not affect execution.                                               |
| `work.Critical()`  | If this task fails, aborts the entire workflow run immediately rather than only failing the caller.                                        |
| `work.Config(str)` | Mixes `str` into the task's execution fingerprint. If `str` changes, cached results in `--continue` and `work.LastRecord` are invalidated. |
| `work.Timeout(d)`  | Cancels `ctx` and fails the task if execution exceeds duration `d`.                                                                        |
| `work.Secret()`    | Excludes the return value from being written to the state journal.                                                                         |
| `work.Doc(str)`    | Single-line description shown in `dot` graphs and `state` listings.                                                                        |

---

## Naming and Hierarchical Paths

Every task runs within the context of the task that invoked it. A task's **path** consists of its name prefixed by all enclosing task names, separated by `/`:

```go
func release(ctx *work.Context) (string, error) {
    // Path: release/build-linux
    work.Do(ctx, "build-linux", func(ctx *work.Context) (string, error) {
        // Path: release/build-linux/compile
        return work.Do(ctx, "compile", compile)
    })
}
```

The path is computed at runtime when `Do` or `Go` is called.

- **Name collisions**: If a loop invokes `work.Do` multiple times with the same name under the same parent, Cascade automatically disambiguates subsequent calls with a numeric suffix (`build`, `build#2`, etc.). Best practice is to assign distinct names dynamically based on the iteration target (e.g., `"build-" + target`).
- **Path identity**: Two calls with the same name under different parents (e.g., `release/build-linux/deps` and `release/build-darwin/deps`) represent distinct tasks with separate logs, journal entries, and cache keys.
- **Sharing results**: To share the output of an expensive operation between multiple tasks, invoke the operation once in the common parent and pass the resulting value to child tasks as an argument.

---

## Managing Concurrency

Use `work.Go` to run tasks concurrently. It starts execution in a new goroutine and returns a typed `*work.Future[T]`:

```go
linux := work.Go(ctx, "build-linux", func(ctx *work.Context) (string, error) {
    return build(ctx, "linux")
})
darwin := work.Go(ctx, "build-darwin", func(ctx *work.Context) (string, error) {
    return build(ctx, "darwin")
})

a, err := linux.Get()
if err != nil {
    return "", err
}
b, err := darwin.Get()
if err != nil {
    return "", err
}
```

### Concurrency Rules

- Calling `(*Future[T]).Get()` blocks until the task completes, fails, or the workflow context is canceled.
- A run does not finish until every task started with `work.Go` has, whether or not its future was read. A workflow that returns the first error among several futures still has the others' results recorded, in the summary, the journal and the event log.
- Concurrency across all `work.Go` calls is bounded by the `--jobs` flag (default is CPU-based; `0` means unbounded).
- Synchronous `work.Do` calls execute inline on the caller's goroutine and do not count toward the `--jobs` concurrency limit.

### Fanning Out Over Collections

```go
building := make(map[string]*work.Future[string], len(platforms))
for _, p := range platforms {
    target := p
    building[target] = work.Go(ctx, "build-"+target, func(ctx *work.Context) (string, error) {
        return build(ctx, target)
    })
}

for _, target := range platforms {
    bin, err := building[target].Get()
    if err != nil {
        return "", err
    }
    // process binary
}
```

---

## Task Results and Completion

A task function returns a typed value and an error:

```go
// Standard success with custom summary:
ctx.Summarize("built %s (14.2 MiB)", binary)
return binary, nil

// Skipped task (counts as success, reported as skipped in UI):
return "", work.Skip("cache already up to date")

// Standard task failure (handled by caller):
return "", err

// Critical failure (stops entire workflow immediately):
return "", work.Fatal(err)
```

### Summary Display Rules

The summary displayed in terminal tables and logs is determined in this order:

1. An explicit message set via `ctx.Summarize(format, a...)`.
2. If `ctx.Summarize` was not called and `T` is a non-empty `string`, the string value itself.
3. In all other cases (e.g., `int`, `bool`, structs, or empty strings), the default string `"done"`.

### Panics

If a task panics, Cascade recovers the panic, records the stack trace in the task log, and returns the panic as an `error`. A panic fails only that specific task unless marked `work.Critical()`.

### Serialization Requirements

Values returned by tasks are serialized as JSON into the state journal. If a return value cannot be serialized to JSON, the task still completes successfully, but its journal record is marked `Partial` and cannot be restored by `work.LastRecord` or `--continue`.

---

## The Context

`*work.Context` wraps Go's standard `context.Context`, providing cancellation support alongside Cascade's logging and metadata facilities:

```go
cmd := exec.CommandContext(ctx, "restic", "backup")
```

### Scoped Logging

```go
ctx.Logf("processing %d files", len(files))         // Written to flow.log and task log
ctx.Warnf("resource missing: %s", path)             // Written at warning level
ctx.Statusf("uploading (%d/%d)", done, total)       // Updates transient progress in live tree
ctx.Summarize("uploaded %d files", total)           // Final summary text for this task
```

### Periodic Progress Monitoring

For long operations that do not report progress line-by-line, use `ctx.Monitor` to query progress at regular intervals:

```go
stop := ctx.Monitor(10*time.Second, func(ctx context.Context) (string, error) {
    return fmt.Sprintf("processed %d records", counter.Load()), nil
})
defer stop()
```

The probe runs on its own goroutine while the task executes. Any data accessed by the probe must be safe for concurrent access.

### Context Helpers

- `ctx.Path()`: Full hierarchical task path (e.g., `release/build-linux`).
- `ctx.Name()`: Local task name (e.g., `build-linux`).
- `ctx.LogPath()`: Filesystem path to the combined log file (`flow.log`).
- `ctx.Started()`: Timestamp when the task started.

---

## Structuring a Workflow

A workflow entrypoint is registered by passing a function to `cli.App.Flow`:

```go
package main

import (
    "github.com/m-d-brown/cascade/cli"
    "github.com/m-d-brown/cascade/work"
)

func release(ctx *work.Context) (string, error) {
    // orchestrate tasks with work.Do and work.Go
    return "v1.0.0", nil
}

func main() {
    cli.Main(cli.App{
        Name: "release",
        Flow: release,
    })
}
```

If the root workflow does not produce a meaningful return value, return a placeholder `(string, error)` or wrap the root call in an anonymous closure.

---

## Execution and Error Handling

Workflows execute strictly according to standard Go control flow:

- When a task returns an error, its caller receives that error like any Go function call.
- If the caller handles the error and continues, subsequent tasks run normally.
- If the caller returns the error, execution halts along that call branch.

### Halting the Entire Run

To abort the entire workflow when a failure occurs:

- Use `work.Critical()` on critical task definitions.
- Return `work.Fatal(err)` to trigger an immediate abort dynamically.
- Pass `--stop-on-error` on the CLI to abort on any task failure.
- Press `q` or `Ctrl-C` in the live terminal view.

### Task Statuses

| Symbol | Status     | Meaning                                              |
| :----: | :--------- | :--------------------------------------------------- |
|  `✓`   | `ok`       | Ran and finished successfully.                       |
|  `○`   | `skipped`  | Task completed with `work.Skip(...)`.                |
|  `⤾`   | `resumed`  | Value restored from an earlier run via `--continue`. |
|  `✗`   | `failed`   | Returned an error, timed out, or panicked.           |
|  `—`   | `canceled` | Canceled before starting due to an abort elsewhere.  |

The exit code is 1 if any task failed or the run was aborted, and 0 on success.

**Plan Mode (`--plan` or `plan`)**: Executing `plan` runs the workflow with `Options.Plan = true` (`run --plan`). This journals the execution trace (allowing `dot` and `flamegraph` to visualize the run), but permanently excludes its records from future resumption via `--continue` or `work.LastRecord`. Note that `--plan` marks journal metadata; suppressing actual external side effects requires wiring a dry-run world (`world.DryRun()`).

---

## Caching and Skipping Work

Cascade does not require a static dependency graph to skip work. You can check previous execution records using `work.LastRecord`:

```go
func snapshot(ctx *work.Context) (string, error) {
    cfg := dest + "|" + every.String()
    if v, meta, ok := work.LastRecord[string](ctx, "snapshot", work.Config(cfg)); ok && meta.HasValue {
        if meta.Age() < every {
            ctx.Logf("snapshot fresh (%s old)", meta.Age())
            return v, nil
        }
    }
    return work.Do(ctx, "snapshot", takeSnapshot, work.Config(cfg))
}
```

### `work.LastRecord` Mechanics

- Looks up the most recent successful run in the state journal matching the task's path and `work.Config` fingerprint.
- Returns `(value, meta, true)` if a matching successful record was found.
- `meta.Age()`: Duration since the recorded task completed.
- `meta.HasValue`: Indicates whether a non-nil serialized value was stored. (Tasks using `work.Secret()` store metadata but omit the value.)

If a task branch is skipped based on `LastRecord`, downstream child tasks are never called, emitting no events and generating no log files.

---

## Side Effects and Dry-Run (The World)

The core `work` package executes Go functions directly. To support dry-run simulation and interactive confirmations, use the [`world`](../world) package to isolate external mutations:

```go
// Void side effect:
err := world.Change(ctx, w, "create directory "+dir, func() error {
    return os.MkdirAll(dir, 0755)
})

// Value-producing effect with fallback:
files, err := world.Perform(ctx, w, world.Effect[[]string]{
    What:    "list remote bucket " + bucket,
    Do:      func() ([]string, error) { return s3List(bucket) },
    Instead: []string{"simulated-file-1.tar.gz"},
})
```

### World Implementations

- **`world.Real()`**: Executes operations directly against the system.
- **`world.DryRun()`**: Suppresses operations, logs `"would: <What>"`, and returns the fallback value specified in `Instead`.
- **`world.Confirm(w, prompter)`**: Wraps an underlying world and prompts the user before each effect (allowing the user to approve, skip, dry-run, or abort).

### Wiring World into the Application

```go
cli.Main(cli.App{
    Name: "deploy",
    Flags: func(fs *flag.FlagSet) {
        fs.BoolVar(&dryRun, "dry-run", false, "simulate execution without making changes")
    },
    Flow: func(ctx *work.Context) (string, error) {
        var w world.World = world.Real()
        if dryRun {
            w = world.DryRun()
        }
        return deploy(ctx, w)
    },
})
```

Cascade's `--plan` flag marks the run in the journal (`Options.Plan`) to prevent future resumption, but does not automatically suppress arbitrary Go side effects. To make a plan run touch nothing, pass a `world.DryRun()` instance into your workflow functions.

---

## Run Persistence and Resumption

Every workflow execution generates an append-only journal in `~/.local/state/cascade/<app>.json` (configurable via `--state`).

- State is checkpointed when a run starts, whenever a task finishes, and when the run completes.
- Interrupted runs can be inspected with `runs` and continued with `--continue`.

```bash
$ release runs
2026-08-29T06-48-57  ✓ ok           v1.4.0                   18 ok                        3.0s
2026-08-29T06-31-27  ✗ failed       v1.4.0                   9 ok, 1 failed              (plan)
                     finish it: release run --continue 2026-08-29T06-31-27
```

### How `--continue` Works

Running `release run --continue <id>` (or `--continue last`) re-executes the workflow function from the top. When the workflow calls `work.Do` or `work.Go`, Cascade checks if that exact path previously succeeded under the same configuration fingerprint. If so, Cascade returns the recorded result immediately instead of re-running the function.

---

## Terminal Displays and Monitoring

When running in an interactive terminal, Cascade renders a live redrawing tree showing task progress, durations, and status updates:

```
release
  ✓ build-linux (1.2s)
  ⠿ build-darwin compiling package 14/82 (3.1s)
  ○ deploy-staging skipped (up to date)
```

- When the run completes in a terminal, the tree remains interactive. Use the arrow keys to navigate tasks and press `Enter` to view a task's full text log. Press `q` or `Esc` to exit to the shell.
- Non-interactive environments (CI, cron, pipes) automatically fall back to streaming line-by-line log output. Pass `--plain` to force line-oriented output explicitly.

---

## Visualizing Traces (DOT & Flamegraphs)

Cascade includes tools to visualize workflow traces from past runs:

### Graphviz DOT Diagram

```bash
release dot [run-id] | dot -Tpng -o trace.png
```

Generates a hierarchical box diagram showing caller-callee relationships, statuses, durations, and summaries.

### Concurrency Flamegraph

```bash
release flamegraph [run-id] > timeline.json
```

Outputs Chrome Trace Event Format JSON. Open [ui.perfetto.dev](https://ui.perfetto.dev) or `chrome://tracing` and drag in `timeline.json` to inspect task concurrency, thread assignments, and execution bottlenecks.

---

## The CLI Framework

Applications built with `cli.Main` provide a standardized set of commands and flags:

### Subcommands

- `run`: Executes the workflow.
- `plan`: Runs the workflow in plan mode (`--plan`), creating a journaled execution trace that will not be resumed by `--continue`.
- `start`: Displays recent runs and prompts for an action (default behavior on interactive terminals when invoked without arguments).
- `runs`: Lists historical runs and their completion statuses.
- `logs [id]`: Displays the combined log for a run.
- `dot [id]`: Outputs a Graphviz DOT visualization of a run's call trace.
- `flamegraph [id]`: Outputs a Chrome Trace Event timeline of a run.
- `state`: Displays current state journal records.

### Common Flags

- `-j, --jobs int`: Maximum number of concurrent tasks started with `work.Go` (default: unbounded).
- `-n, --plan`: Mark run as a plan; journaled but excluded from future resumption.
- `--continue string`: Resume an interrupted run by ID or `"last"`.
- `--stop-on-error`: Abort the workflow immediately on the first task error.
- `--plain`: Disable live redrawing and stream log output line by line.
- `-v, --verbose`: Include transient progress updates in plain-text output.
- `--state path`: Path to state journal file (use `--no-state` to disable state persistence).
- `--log-dir path`: Directory storing per-run logs.

---

## Batching Operations

To optimize operations that are cheaper to perform in batches (such as retrieving multiple credentials in a single network request), define a function that accepts a collection:

```go
values, err := units.Secrets(ctx, w, vault, "secrets", map[string]string{
    "db-password": "op://Production/database/password",
    "api-key":     "op://Production/api/key",
})
```

**Ordering:** Invoke batch operations in the parent task before launching the concurrent child tasks that consume the individual items.

---

## Running External Commands

Use [`units.Run`](../units/exec.go) to execute external processes with integrated logging, cancellation, and dry-run interception:

```go
res, err := units.Run(ctx, w, units.Cmd{
    Path:     "restic",
    Args:     []string{"backup", sourceDir},
    Env:      append(os.Environ(), "RESTIC_PASSWORD="+password),
    Instead:  []string{"snapshot 4b1d9f2a saved"},
})
```

- Output is automatically streamed to the task's log file.
- Set `Quiet: true` to route command output to transient status updates instead of persistent logs.
- Use `AllowExit: []int{...}` to treat specific non-zero exit codes as successful completions.

---

## Operational Considerations and Gotchas

1. **Path stability in resumption**: `--continue` re-enters the workflow function from the beginning and matches completed steps by hierarchical path. If task names or nesting structures change between runs, earlier records will not match and tasks will re-execute.
2. **Task deduplication**: Cascade does not automatically deduplicate tasks invoked with the same name across separate goroutines. If two parallel tasks require the same expensive resource, compute it in the common parent task and pass the result to both branches.
3. **Local fingerprinting**: `work.Config` fingerprints apply only to the specific task that declares them; they are not transitively propagated to caller or callee tasks. If a task's validity depends on an upstream value, incorporate that value into its own `work.Config`.
4. **Dynamic error discovery**: Because workflows are standard Go code rather than static graphs, misconfigurations (such as typos in task names) are discovered when the code path executes. Use unit tests to validate workflow branches.
5. **Plan vs. dry-run**: `--plan` marks the run metadata in the journal so it cannot be resumed. Suppressing real-world side effects requires wiring a `world.DryRun()` instance into your workflow functions.
6. **Goroutine deadlocks under bounded jobs**: Tasks started with `work.Go` acquire a concurrency slot before starting. To avoid deadlocks when `--jobs` is constrained, synchronize concurrent tasks using `Future.Get()` rather than unmanaged Go channels.
7. **API drift validation**: Run `task api:check` (or `go generate ./internal/api`) after changing exported symbols to ensure `api.txt` matches the public package surface.
