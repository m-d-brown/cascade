# Design Decisions

This document details the architectural rationale and engineering trade-offs behind Cascade. Each section explains why a decision was made, how it is implemented, and the trade-offs it introduces.

- [Prefer Functional Style](#prefer-functional-style)
- [Dynamic Execution over Declared Graphs](#dynamic-execution-over-declared-graphs)
- [Explicit Result Sharing](#explicit-result-sharing)
- [Strongly Typed Outputs](#strongly-typed-outputs)
- [Direct Execution Visibility](#direct-execution-visibility)
- [Side-Effect Interception via World](#side-effect-interception-via-world)
- [Checkpointable and Resumable Runs](#checkpointable-and-resumable-runs)
- [Path as Runtime Identity](#path-as-runtime-identity)
- [Decoupled Execution and Observability Packages](#decoupled-execution-and-observability-packages)
- [Minimal Exported API Surface](#minimal-exported-api-surface)
- [Fail-Closed Semantics](#fail-closed-semantics)
- [Declarative Pipelines as an Interpreter Layer](#declarative-pipelines-as-an-interpreter-layer)

---

## Prefer Functional Style

**Core principle:** Prefer immutable values and pure functions over shared mutable state.

Tasks accept inputs as standard arguments and return outputs as typed values. Intermediate results that accumulate across operations do so by returning new data structures rather than mutating shared memory in place. This makes task composition predictable: independent calls can execute safely in any order without synchronization risks.

### Where This Applies
- **`work.Do` and `work.Go`**: Task return values are returned directly as typed `T` instances without mutating intermediate container types.
- **`work.Future[T]`**: Represents an immutable result value once `.Get()` resolves. Reading the future does not alter its state.
- **`history.Result`, `history.TaskResult`, `history.Record`**: Built once when a task or run completes and treated as immutable thereafter.
- **`work.Context`**: A distinct context instance is allocated for each task invocation, preventing task identity, paths, or logging buffers from leaking to sibling tasks.

### Deliberate Exceptions
- **Runner state**: The internal `work.Runner` manages mutable runtime state, including active task statuses, in-flight trees, and journal writes. This state is strictly private to `work.Runner` and guarded by a mutex.
- **Workflow-local state**: A workflow function is free to maintain standard Go local variables (e.g., slices of futures, error collections, or configuration maps) across task calls, following standard Go practices.

---

## Dynamic Execution over Declared Graphs

In Cascade's engine, a workflow is written as a standard Go function. Control flow, execution sequence, and concurrency follow the program's normal `if` conditions, loops, and goroutines. There is no pre-compilation step, no dependency builder, and no abstract graph object separate from the executing code.

### Trade-offs & Invariants
- **Runtime validation**: Without a static graph to inspect before execution, errors such as invalid task names or nil functions are detected when the code executes rather than during a preflight compile phase.
- **Trace-driven visualization**: Commands such as `plan` and `dot` visualize runs by inspecting recorded execution traces rather than an abstract graph definition. `plan` is `run --plan`: executing the workflow with `Options.Plan = true` records its shape in the journal without committing resumable work. `dot` reads the journal of a completed run to construct the execution hierarchy.
- **Selection via standard flags**: Cascade does not implement custom DSL syntax for task selection (e.g., `-s`/`-x` flags). Instead, developers expose standard CLI flags that conditionally branch or filter calls directly in Go.

---

## Explicit Result Sharing

When two separate tasks require the result of a shared prerequisite, Cascade does not automatically fold duplicate task names into a single shared node. Two distinct calls to the same function name under different parents generate distinct hierarchical paths (for example, `release/build-linux/toolchain` and `release/build-darwin/toolchain`).

### Design Pattern

To share results across tasks, invoke the shared operation once in the common parent task and pass the returned value down to child tasks:

```go
func release(ctx *work.Context) (string, error) {
    // Install toolchain once:
    toolchain, err := work.Do(ctx, "prepare", prepare)
    if err != nil {
        return "", err
    }

    // Pass toolchain output into concurrent child tasks:
    linux := work.Go(ctx, "build-linux", func(ctx *work.Context) (string, error) {
        return build(ctx, toolchain, "linux")
    })
    darwin := work.Go(ctx, "build-darwin", func(ctx *work.Context) (string, error) {
        return build(ctx, toolchain, "darwin")
    })
    ...
}
```

This pattern aligns with standard Go programming conventions, making data flow explicit and avoiding hidden framework-level caching magic.

---

## Strongly Typed Outputs

`work.Do[T]`, `work.Future[T]`, and `world.Effect[T]` leverage Go generics to preserve return types. Calling code receives the concrete type `T` directly, avoiding runtime type assertions or reflection.

### Serialization Handling

When values are restored from the state journal (via `work.LastRecord` or `--continue`), they are unmarshaled from JSON. An integer stored in JSON might deserialize as a generic number type. Cascade's internal `decodeAs[T]` automatically normalizes these JSON-restored values back into the expected target type `T`. If a stored value cannot be converted to `T` (for example, if the function signature changed between runs), the task panics with a type mismatch error, which Cascade catches and reports as a task failure.

---

## Direct Execution Visibility

In Cascade, task execution strictly mirrors Go control flow. When an `if` condition evaluates to false, the enclosed task calls are not invoked:

```go
func snapshot(ctx *work.Context) (string, error) {
    if v, meta, ok := work.LastRecord[string](ctx, "snapshot", work.Config(cfg)); ok && meta.Age() < every {
        return v, nil
    }
    return work.Do(ctx, "snapshot", takeSnapshot, work.Config(cfg))
}
```

### Invariants
- **No phantom task records**: An unreached branch emits no events, creates no log files, and appears nowhere in `dot` graphs or terminal summaries.
- **Prerequisite evaluation**: Caching decisions must be self-contained (evaluating file timestamps, database state, or journal records) rather than invoking downstream tasks to decide whether upstream tasks are needed.
- **Explicit failure propagation**: When a task returns an error, callers decide whether to handle it or return early. Uncalled downstream tasks are simply not invoked. (Use `work.Critical()` or `work.Fatal()` when a failure should abort the entire run.)

---

## Side-Effect Interception via World

The core `work` package deliberately has no knowledge of external environments or filesystem abstractions; it simply executes Go functions. The [`world`](../world) package provides an optional abstraction layer for intercepting external side effects.

### Architecture

Tasks describe proposed operations and pass them along with a `world.World` instance to `world.Change` or `world.Perform`:

```go
world.Change(ctx, w, "create "+dir, func() error {
    return os.MkdirAll(dir, 0755)
})
```

- In standard runs, `world.Real()` executes the operation directly.
- In dry-run mode, `world.DryRun()` logs `"would: <operation>"` and returns a configured fallback value (`Instead`).
- In interactive mode, `world.Confirm(w, prompter)` prompts the user before executing the operation.

`World` instances are passed explicitly as normal arguments, ensuring that callers retain full visibility and control over where side effects occur.

The CLI's `plan` command (`run --plan`) marks a run in the journal (`Run.Plan = true`) but does not construct or inject a `World`. Applications opt into dry-run safety by defining their own `--dry-run` flag and wiring `world.DryRun()` accordingly. Combining both (`plan --dry-run`) provides a disposable run trace that also performs no external modifications.

---

## Checkpointable and Resumable Runs

Runs are assigned unique timestamps and identifiers, and state is written to an append-only journal:
1. When the run begins.
2. Whenever any task completes, fails, or is skipped.
3. When the entire run finishes.

### Invariants
- **Resumption keyed by path**: `--continue` identifies previous results by matching the task's full hierarchical path and `work.Config` fingerprint.
- **Plan runs excluded from resumption**: Runs executed with `--plan` (or `plan`) are recorded with `Run.Plan = true`. Cascade unconditionally excludes plan records from being restored by `--continue` or `work.LastRecord`.
- **Atomic state checkpoints**: The journal records the complete status of the workflow at every checkpoint, ensuring interrupted runs (such as from power loss or user cancellation) can be reliably resumed.

---

## Path as Runtime Identity

A task's identity is defined by its full path, constructed dynamically from its name and the names of its enclosing tasks.

- **Dynamic path resolution**: Paths are evaluated when `work.Do` or `work.Go` executes, requiring no separate registration or manifest.
- **Consistent cross-tool addressing**: The hierarchical path serves as the uniform key across all Cascade features: the terminal tree, log files, event streams, Graphviz DOT nodes, and flamegraph spans.

---

## Decoupled Execution and Observability Packages

Cascade maintains a strict boundary between task execution and run observability:

- **`work`**: Contains only the core execution primitives: `Context`, `Do`, `Go`, `Future`, and options (`Timeout`, `Critical`, `Config`, `Secret`). It has no dependency on terminal rendering, Graphviz generation, or CLI flag parsing.
- **`history`**: Owns data models representing recorded runs: `Status`, `Record`, `Run`, `Journal`, `Event`, `Dot`, and `Flamegraph`. Types in `history` are pure data structures or functions operating on data structures, with no dependencies on `work.Runner`.
- **`ui` and `cli`**: Consume `history` events and records to render terminal views and provide the command-line interface.

This decoupling allows developers to inspect or integrate with Cascade's data structures without pulling in terminal or execution dependencies.

---

## Minimal Exported API Surface

Cascade maintains a minimal exported surface across its public packages (`work`, `history`, `world`, `cli`, `units`, `ui`, `interpreter`).

- **Verification via `api.txt`**: All exported symbols are tracked in `api.txt`. A continuous integration test validates that the codebase matches `api.txt` exactly.
- **Explicit visibility**: Internal helpers and structures without callers outside their package remain unexported, ensuring long-term API stability.

---

## Fail-Closed Semantics

Cascade prioritizes safety over silent continuation:

- **Non-serializable values**: If a task output cannot be serialized to JSON, it is marked as `Partial` in the journal and excluded from future resumption.
- **Secret values**: Tasks marked `work.Secret()` are never written to the journal, requiring them to re-execute in subsequent runs.
- **Plan run isolation**: Records from a plan run (`Run.Plan = true`) are permanently excluded from `Restorable` and `LastRecord`, regardless of task completion status.
- **Binary invalidation**: Task fingerprints incorporate the hash of the compiled application binary. If the binary changes, past records are invalidated.
- **Strict type checking**: If a deserialized journal value does not match the expected type `T` requested by `work.LastRecord`, Cascade aborts the lookup with an error rather than returning zero values.
- **Panic containment**: Panics inside a task function are caught and converted into a task error with stack trace logging, preventing uncontrolled process termination.

---

## Declarative Pipelines as an Interpreter Layer

While dynamic Go code provides full flexibility, many operational workflows (such as build scripts, backups, and deployments) consist of fixed shell commands with predictable dependencies. Writing these workflows in Go can introduce unnecessary boilerplate.

The `interpreter` package provides a YAML front end for declaring these pipelines:
- **Interpreter architecture**: `interpreter.Plan` compiles YAML files into waves of `work.Go` and `work.Do` calls, reusing Cascade's existing runner, journal, terminal UI, and logging.
- **Upfront validation**: Because YAML definitions are static data, `cascade check` can perform comprehensive preflight validation (detecting cycles, dangling dependencies, and syntax errors) before any commands execute.
- **Declared status reporting**: In YAML pipelines, all declared actions appear in the terminal tree, and actions whose dependencies fail are explicitly reported as `Skipped (blocked)`.

When pipelines outgrow static graphs and require conditional branches, dynamic fan-out, or programmatic data processing, developers can migrate from YAML to the Go engine without changing underlying runtime tooling.
