# examples/cascade

A build pipeline declared in [`cascade.yaml`](cascade.yaml), for trying the
`cascade` binary on. Every action is a shell one-liner over coreutils, so it
runs anywhere with a shell — nothing to install.

```
checkout ─► warmup ─► deps ─┬─► compile ─► test ─┐
                            └─► lint ────────────┴─► package ─► release
```

## Run it

Build the binary first. Under `go run`, every invocation is a freshly linked
binary with a new fingerprint, so nothing is ever seen as up to date.

```
go build -o /tmp/cascade ./cmd/cascade
cd examples/cascade

/tmp/cascade run          # first time: every action runs
/tmp/cascade run          # again: everything up to date, 0 ran
touch build/src/main.txt
/tmp/cascade run          # the change cascades: warmup, deps, compile, test,
                          #   package re-run; checkout and lint do not
```

`warmup` sleeps for six seconds; from another terminal, while a run is going:

```
/tmp/cascade status       # where it has got to — reads the live event log
/tmp/cascade status -w     # …refreshing until it finishes
```

More:

```
/tmp/cascade check            # validate the file, report anything suspect
/tmp/cascade run --dry-run    # walk the whole thing, touching nothing
/tmp/cascade plan             # a dry run, also journaled (never resumed from)
/tmp/cascade run --confirm    # ask before every action
/tmp/cascade runs             # past runs
/tmp/cascade dot | dot -Tsvg -o trace.svg     # the run that just happened
/tmp/cascade graph -f cascade.yaml | dot -Tsvg -o dag.svg   # the declared graph
```

`graph` and `status` are small extra subcommands in
[`cmd/cascade/main.go`](../../cmd/cascade/main.go) — `cli.App` has no hook for
one, so they are handled before the framework sees the arguments. `graph`
prints the `needs` DAG, which the engine's `dot` cannot (there the actions are
siblings under one root).
