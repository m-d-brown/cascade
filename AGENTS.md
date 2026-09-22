# Agent instructions for cascade

This file is for any coding agent working in this repo (Claude, Gemini, or otherwise). Humans: the same gate applies to you. See "Before committing" below, and run `task hooks:install` once so it happens automatically.

## What this project is

cascade has two interfaces sharing one Go module: a Go engine for pipeline-oriented work (`work`, `history`, `world`, `cli`, `units`, `ui`, at the top level), and a YAML front end built on it (the `interpreter` package and `cmd/cascade`). In the engine, a workflow is a Go function coordinating tasks through `work.Do` and `work.Go`, with execution order and concurrency determined by standard Go control flow. The front end allows declaring that same style of pipeline in YAML for fixed command pipelines (see `docs/pipeline-spec.md` and `docs/design.md#declarative-pipelines-as-an-interpreter-layer`).

Start with `README.md`; `docs/guide.md` is the reference for the engine side, `docs/pipeline-spec.md` documents the YAML format, and `docs/design.md` details the architectural trade-offs and design invariants.

Where a change usually belongs:

| Changing…                                                                  | Start at                                                                |
| -------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| what a call can do (`Secret`, `Critical`, `Timeout`, `Config`, resumption) | `work/do.go`, `history/store.go`                                        |
| a run's logs, journal or event stream                                      | `history/logdir.go`, `history/store.go`, `history/event.go`             |
| what a call's effects can do, or dry-run/confirm                           | `world/world.go`, `world/approval.go`                                   |
| a CLI subcommand or flag                                                   | `cli/cli.go`; the front door (`start`) is `cli/start.go`                |
| the live terminal tree or the summary table                                | `ui/live.go`, `ui/tree.go`, `ui/summary.go`                             |
| external commands or secrets from a call                                   | `units/exec.go`, `units/secret.go`                                      |
| the YAML front end (schema, freshness, `cascade check`/`status`)           | `interpreter/` (start with `interpreter/doc.go`), `cmd/cascade/main.go` |
| a worked example of the engine                                             | `examples/engine/complete/main.go`                                      |

## Before committing

Run this before every commit. It is the one command that replaces remembering `go fmt` / `go vet` / `go test` / lint separately, and it is exactly what CI runs:

```
task precommit
```

It runs, in order: `gofmt` check, `prettier` check, `go vet`, a build, `golangci-lint`, the `api.txt` drift check, and the test suite with the race detector.

If a step fails, fix it and run `task precommit` again before committing. Do not commit past a red gate.

Don't have `task` (https://taskfile.dev)? Install it with `go install github.com/go-task/task/v3/cmd/task@latest`. See `Taskfile.yml` for the individual tasks (`fmt`, `vet`, `build`, `lint`, `test`, `tidy`, `api`) if you only need one of them.

To make this automatic on `git commit`, run once: `task hooks:install`. It points `core.hooksPath` at `.githooks`, which runs `task precommit`. No other task runs this for you, since it edits repo-local git config: that is a decision for whoever owns the checkout to make, not a side effect of testing.

## Documentation and voice guidelines

When writing documentation, guides, commit messages, or package godoc, write as an experienced systems engineer communicating directly with peers. Avoid the stylized, pseudo-philosophical tone typical of raw LLM outputs:

- **State what it does directly (avoid compulsive antithesis):** Don't define concepts primarily by what they are not ("This is not a workaround; it is..."). State the behavior and purpose positively and directly.
- **Avoid epigrams and pseudo-profundity:** Cut phrases like "because that is what it is", "a capital letter is a promise", or "the honest rendering of a declaration is its whole self". Focus on technical specifics.
- **Vary structure (break the "cost vs. buys" formula):** Don't frame every decision with "The cost is X; what it buys is Y". Use standard headings like "Rationale", "Invariants", and "Trade-offs", or concise bullet points.
- **Use standard engineering verbs:** Use _returns_ instead of _hands back_; _calls_, _uses_, or _imports_ instead of _reaches for_; _completes_ or _finishes_ instead of _settles_; _inspect progress_ instead of _where a run has got to_.
- **Avoid em-dash (`—`) overuse:** Don't rely on em-dashes in every sentence for dramatic pause. Use periods, commas, or structured lists.
- **Don't lecture on standard Go:** Assume the reader knows Go. Explain Cascade's abstractions (`work.Do`, `Context`, `LastRecord`, `World`) rather than re-explaining how `if err != nil` or standard function arguments work.
- **Check code before stating behavior:** Never sacrifice technical accuracy for a punchy sentence. Check actual function signatures and type checks before documenting behaviors.

## Things that will otherwise surprise you

- **`api.txt` is generated and checked in CI.** It is every exported identifier of `work`, `history`, `world`, `cli`, `units`, `ui` and `interpreter`. Change an exported name and forget to regenerate it, and CI fails on drift. `task api:check` catches this locally.
- **There is no declared graph.** `check`, `list`, `explain`, and `-s`/`-x` selection don't exist because there is no shape to check, list, explain, or select from before a run happens. `plan` is `run --plan`. `dot` draws a specific run's trace read from the journal after the fact. See `docs/design.md#dynamic-execution-over-declared-graphs`.
- **Sharing work between two calls means calling it once and passing the result down.** Calling it from both places makes two separate calls; there is no mechanism that folds same-named calls together. See `docs/design.md#explicit-result-sharing`.
- **A commit that does two things gets described as the smaller one.** Keep unrelated changes in separate commits.
- **`interpreter/` is a YAML front end. It is not part of the engine.** It runs a declared graph of shell actions by driving `work.Go`/`work.Do`: an interpreter on top of the engine, not a second one. It brings back `needs`, blocked dependents, and waves on purpose. See `docs/design.md#declarative-pipelines-as-an-interpreter-layer`. It is the only package that pulls in a YAML parser.
- **The exported API is the smallest thing that works.** Don't export something with no caller outside its own package on the assumption it might be useful later.
- **README.md's images are generated, not hand-made.** `scripts/screenshots.sh` runs `examples/cascade` twice and `examples/engine/complete` once in an isolated temp directory. The two cascade-run images are the live tree, not `cmd/cascade`'s own `--plain` summary. Capturing the live tree needed a workaround for two freeze v0.2.2 bugs: `--lines` panics past a handful of skipped lines, and `-x` can't send the child any input, so nothing can dismiss the tree's "press q to exit" prompt through freeze alone. `scripts/tree_demo.go`, `scripts/pty_capture.py`, and `scripts/last_frame.py` are that workaround; read their own doc comments before touching any of this. Re-run the whole script after any change that would change what the images show (the live tree's layout or colors, either example), check the results before committing, and commit them alongside the change that prompted them.
