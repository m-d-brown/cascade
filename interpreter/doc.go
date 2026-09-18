// Package interpreter runs a graph of shell commands declared in YAML as a
// workflow.
//
// The framework underneath has no graph on purpose: a workflow is a plain Go
// function, and what runs is exactly what the code calls (see
// github.com/mdbrown/cascade/docs/design.md#there-is-no-graph). That is
// the right shape when a pipeline has real control flow — conditionals,
// loops, fan-out computed at run time. A large class of operational work is
// not that: it is a fixed set of external commands, each waiting on some
// others, each with a way to tell whether it already ran. Writing that by
// hand is a lot of
//
//	work.Go(ctx, "name", func(ctx *work.Context) (T, error) { … }, opts…)
//
// for a structure that is really just data. This package is the data: a
// cascade file lists the actions, their dependencies, and how each decides it
// is up to date, and [Plan.Run] drives work.Go/work.Do on the author's
// behalf — so the whole of the framework's value (the live tree, the journal,
// flow.log, dot, flamegraph, --continue, and approval prompts) is inherited
// with no new engine code. It is an interpreter on top of the engine, not a
// change to it.
//
// # The file
//
//	name: build                      # optional — defaults to the file's base name
//
//	actions:
//	  checkout:
//	    run: git fetch && git reset --hard origin/main
//
//	  deps:
//	    run: go mod download
//	    needs: [checkout]
//
//	  compile:
//	    run: go build -o build/app ./cmd/app
//	    needs: [deps]
//	    produces: [build/app]         # up to date if this exists and is newer …
//	    sources: ["**/*.go"]          # … than every source
//
//	  snapshot:
//	    run: restic backup ~
//	    every: 24h                    # skip if it last succeeded < 24h ago
//
//	  release:                        # empty run: a barrier that only waits on needs
//	    needs: [compile, snapshot]
//
// The action name is the map key — nothing is repeated. Every field but the
// key is optional: run, needs, produces, sources, every, timeout (kill the
// command after a duration), unless (a shell command; exit status 0 means
// "already done"), env (overrides on top of the process environment), dir,
// allow-exit ([]int), optional (a failure becomes a skip), progress /
// progress-every, and description. needs, produces and sources take a bare
// string as a one-element list.
//
// [Load] rejects what cannot run — an unknown field, a dangling or cyclic
// need, an unparseable duration, an action defined twice, `run` given a list
// — with a line number and a message that names the file's own vocabulary.
// What runs but is probably a mistake — a barrier that also sets freshness
// fields, an impossible allow-exit code, two actions that produce the same
// path — is a [Plan.Warnings] entry, logged before a run starts and printed
// by `cascade check`.
//
// # Freshness
//
// An action runs unless it is proven fresh. Each signal it configures can
// prove freshness; when it sets more than one, all must agree before the
// action is skipped — the conservative direction, matching
// design.md#fail-closed. A fresh action reports [work.Skip], so every
// declared action still shows up as a row: in a declared graph, seeing each
// action's status is the honest rendering, where in a Go-authored workflow an
// unreached call simply never happens.
//
//   - every: the run journal shows this action last succeeded, under the same
//     fingerprint, less than the given duration ago.
//   - produces + sources: every produces path exists and is newer than every
//     sources path (a "**" path segment matches any depth). A missing output
//     is stale.
//   - unless: the command exits 0.
//
// Each check is routed through the run's [world.World], so under a dry-run
// world every check reports "stale" and `plan --dry-run` shows the whole
// cascade.
//
// A stale action re-runs. If what it produces is another action's sources,
// that action is then stale in turn — a change near the top cascades forward
// through the graph, which is the name.
//
// # What you give up
//
// No conditionals, no loops, no fan-out whose shape depends on a value
// computed mid-run, and dot draws the run trace as a flat list rather than
// the dependency DAG (the actions are siblings; nothing nests). For any of
// those, write the workflow in Go and call this package's engine directly.
// What you get back is a pipeline that reads top to bottom as the thing it
// is, and that anything — not just a Go compiler — can inspect.
package interpreter
