# examples/cascade

A build pipeline declared in [`cascade.yaml`](cascade.yaml), useful for trying out the `cascade` command-line tool. Every action is a shell one-liner using core utilities, so it runs on any POSIX system without extra dependencies.

```
checkout ─► warmup ─► deps ─┬─► compile ─► test ─┐
                            └─► lint ────────────┴─► package ─► release
```

## Running the Example

Build the `cascade` binary first. (Running via `go run` produces a newly linked binary on each invocation with a different build fingerprint, preventing caching checks from recognizing past runs.)

```bash
go build -o /tmp/cascade ./cmd/cascade
cd examples/cascade

/tmp/cascade run          # first run: all actions execute
/tmp/cascade run          # second run: all actions are skipped as up to date
touch build/src/main.txt
/tmp/cascade run          # the touched file triggers: warmup, deps, compile, test,
                          # and package re-run; checkout and lint remain skipped
```

The `warmup` action includes a short sleep. You can inspect its progress from a second terminal:

```bash
/tmp/cascade status       # inspect current progress from the live event log
/tmp/cascade status -w    # watch live progress updates until completion
```

### Additional Commands

```bash
/tmp/cascade check            # validate pipeline syntax and report warnings
/tmp/cascade run --dry-run    # execute pipeline with side effects suppressed
/tmp/cascade plan             # preview the execution trace without committing changes
/tmp/cascade run --confirm    # interactively prompt before each action
/tmp/cascade runs             # display run history and resumption status
/tmp/cascade dot | dot -Tsvg -o trace.svg                   # render the execution trace
/tmp/cascade graph -f cascade.yaml | dot -Tsvg -o dag.svg   # render the declared dependency graph
```

Note: `status` and `graph` are custom commands implemented in [`cmd/cascade/main.go`](../../cmd/cascade/main.go) specifically for YAML pipelines. `graph` prints the declared dependency DAG, whereas the engine's `dot` command visualizes the runtime call trace of a specific run.
