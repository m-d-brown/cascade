# The cascade guide

Run a graph of shell commands, declared in YAML, as a workflow. The
[README](../README.md) is the short version; this is the whole of it —
the file format, freshness rules, and `cascade status`. For writing a
workflow directly in Go instead, see [the engine guide](guide.md).

The framework has [no graph](design.md#there-is-no-graph) on purpose — a
workflow is a plain Go function. That is the right shape when a pipeline has
real control flow. A backup, a deploy, a nightly build is not that: it is a
fixed set of external commands with dependencies and "did this already run?"
checks, and writing it as Go is boilerplate around a structure that is really
just data. A cascade file is the data; the [`interpreter`](../interpreter)
package drives `work.Go`/`work.Do` on your behalf, so the live tree, the
journal, `flow.log`, `dot`, `flamegraph`, `--continue` and approval prompts
all still come for free.

```go
cli.Main(cli.App{
    Name: "build",
    Flow: func(ctx *work.Context) (string, error) {
        r, err := interpreter.Load("build.yaml")
        if err != nil {
            return "", work.Fatal(err)
        }
        return r.Run(ctx, world.Real())
    },
})
```

The `cascade` binary — [`cmd/cascade`](../cmd/cascade) — is exactly that App
over any file you point `-f` at:

```
go install github.com/mdbrown/cascade/cmd/cascade@latest
cascade check -f build.yaml   # validate before you run
cascade run -f build.yaml
```

[`examples/cascade`](../examples/cascade) has a self-contained cascade to try
it on.

## The file

```yaml
name: build                      # optional — defaults to the file's base name

actions:
  <name>:                        # the key is the action's name
    run: <shell command>         # run through "sh -c"; empty = a barrier
    needs: [<name>, …]           # actions that must finish first
    produces: [<path>, …]        # freshness: this action's outputs
    sources: [<glob>, …]         # freshness: its inputs ("**" = any depth)
    every: <duration>            # freshness: e.g. "24h", "90m"
    timeout: <duration>          # kill run after this; the action fails
    unless: <shell command>      # freshness: exit 0 ⇒ already done
    env: {KEY: VALUE, …}         # added on top of the process environment
    dir: <path>                  # working directory
    allow-exit: [<int>, …]       # non-zero exit codes that are not failures
    optional: true               # a failure becomes a skip
    progress: <shell command>    # polled while run is in flight; its last line
    progress-every: <duration>   #   becomes the live status (default 15s / 2s)
    description: <one line>      # shown by `dot` and `state`
```

Every field but the key is optional. `needs`, `produces` and `sources` accept
a bare string as a one-element list; a leading `~` in a path is expanded the
way the shell expands it for `run`.

## Catching mistakes

`cascade check` parses and validates a file without running anything. Some
things are **errors** — the cascade cannot run, reported with a line number
and the file's own vocabulary:

- an unknown field (the message lists the valid ones), `run` given a list,
  `actions` given a list, an action defined twice
- an action name with a `/` or surrounding whitespace
- a `needs` that names no action, a self-need, or a cycle (named: `a → b → a`)
- an unparseable `every` / `progress-every` duration
- a file that is missing, a directory, or empty

Others are **warnings** — it runs, but you probably didn't mean it;
`Plan.Warnings()` / `cascade check` report every one, and `cascade run` logs
them before it starts:

- an action with no `run` and no `needs` (it does nothing)
- a barrier (no `run`) that also sets `produces`, `every`, … (all ignored)
- `sources` without `produces` (the sources are never checked)
- `every` that isn't positive (never skips)
- an `allow-exit` code of `0`, or one outside 0–255
- a `needs`, or a `produces` path, listed twice; two actions with the same
  `produces` path
- a `produces` entry that looks like a glob (they are literal)
- an `env` name containing `=`
- a `dir` that does not exist and no earlier action creates
- a `sources` `**` pattern rooted at `/` or `~` (walks a huge tree each check)

And a few conditions only show up mid-run, as warnings on the action's row:

- the command finished but did not create a file it `produces` — the reason
  it would otherwise re-run every time (usually a typo in the path)
- a `sources` glob matched nothing, so a change to those inputs goes unseen
- an `unless` check that could not run — the action is taken as stale
- a `produces` path that is a directory (its mtime doesn't track its contents)
- a missing working directory — a clear message, not a raw `chdir` error

## Freshness

An action **runs unless it is proven fresh.** Each signal it sets can prove
freshness; when it sets more than one, all must agree before the action is
skipped (the conservative direction). A fresh action reports `work.Skip`, so
it still shows as a row — in a declared graph, seeing each action's status is
the honest rendering.

| signal | fresh when |
| --- | --- |
| `every` | the journal shows this action last **succeeded** less than the duration ago, under an unchanged fingerprint. A skip does not count, so the age is always measured from the last real run. |
| `produces` + `sources` | every `produces` path exists and is newer than every file matched by `sources`. A missing output is stale. Checked after the action's dependencies finish, since an input may be produced upstream. |
| `unless` | the command exits 0. |

Each check runs through the run's world, so under a dry-run world every check
reports "stale" and `plan --dry-run` walks the whole cascade.

The fingerprint that `every` and `--continue` compare against covers an
action's `run`, `env`, `dir` and `needs` — edit any of those and that action
runs again. Freshness settings are deliberately left out. The framework also
mixes the binary into every fingerprint, so **a rebuilt runner re-runs
everything once** — build the runner rather than `go run`-ing it if you want
freshness to hold across invocations.

## run, dry-run, plan

Three different things, the same as anywhere else in the framework:

| | what it does |
| --- | --- |
| `run` | run for real |
| `run --dry-run` (your flag) | route every action through `world.DryRun` — nothing happens, every freshness check reports stale, every action is shown as "would run" |
| `plan` (`run --pretend`) | journal the run and mark it so it is never resumed from; the world is still whatever your `Flow` built |
| `plan --dry-run` | both |

The `cascade` binary wires `plan` to imply the dry-run world, so a plan is
always safe to type; it does this by checking the arguments, the pattern
`world` documents.

On a terminal, `run` shows a live tree while the actions run and then returns
to the shell the moment it's done — it doesn't hold the terminal open for you
to browse the finished tree the way a hand-authored workflow's live display
does, since a cascade's actions are all known already and the summary table
it prints already says what happened. `cascade status` (below) is the tool
for looking at a run afterward.

## Watching a long run

While an action runs, its live status is its own last line of output, or — if
it sets `progress:` — that command's output, polled on an interval. This shows
on the action's row in the live tree.

`cascade status` prints where the last run (or one still in flight) has got to,
read from its event log so it is current mid-run:

```
$ cascade status
run 2026-09-03T07-09-13 · running · started 4s ago
  ✓ checkout  ok          2ms  wrote build/src/main.txt
  • warmup    running      4s  cache 66% warm

$ cascade status -w         # …and keep refreshing until it finishes
$ cascade status <run-id>   # a specific past run
```

`ReadProgress` is the function behind it, for building your own view.

## What you give up

No conditionals, no loops, no fan-out whose shape depends on a value computed
mid-run, and `dot` draws the actions as siblings because nothing nests
(`cascade graph` prints the declared `needs` DAG instead). For any of that,
write the workflow in Go.
