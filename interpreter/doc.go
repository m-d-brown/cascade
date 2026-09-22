// Package interpreter executes declared pipelines of shell commands using
// the cascade engine.
//
// While the core cascade engine runs workflows written as standard Go code,
// many operational tasks (such as build scripts, backups, and deployments)
// consist of fixed shell commands with static dependencies. The interpreter
// allows declaring these pipelines in YAML.
//
// When running a pipeline, [Plan.Run] organizes actions into topological waves
// and executes them concurrently using work.Go and work.Do. This integrates
// YAML pipelines directly into the engine's runtime features: live terminal
// tree display, event logging, crash resumption (--continue), and Graphviz
// exports.
//
// # Pipeline Format
//
//	name: build                      # optional: defaults to file basename
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
//	    produces: [build/app]         # fresh if newer than all sources
//	    sources: ["**/*.go"]
//
//	  snapshot:
//	    run: restic backup ~
//	    every: 24h                    # skip if last succeeded < 24h ago
//
//	  release:                        # barrier: waits for dependencies
//	    needs: [compile, snapshot]
//
// Each action is identified by its map key. Available fields include run,
// needs, produces, sources, every, timeout, unless, env, dir, allow-exit,
// optional, progress, progress-every, and description.
//
// [Load] validates the pipeline syntax, rejecting cycles, undefined needs,
// duplicate names, or malformed durations. [Plan.Warnings] reports valid but
// potentially erroneous configurations, such as barrier actions with unused
// freshness fields.
//
// # Freshness Rules
//
// An action runs unless it is determined to be up to date. If multiple
// freshness checks are defined, all active checks must agree before the
// action is skipped:
//
//   - every: The execution journal indicates this action succeeded within the
//     specified duration under an identical configuration fingerprint.
//   - produces and sources: Every produces file exists and is newer than every
//     file matched by sources.
//   - unless: The specified shell command exits with status code 0.
//
// When an action executes, any downstream action that consumes its outputs as
// sources is automatically marked stale and will also run.
//
// For complete documentation of the YAML configuration format, see
// docs/pipeline-spec.md in the repository root.
package interpreter
