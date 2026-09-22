# cascade

See `AGENTS.md` for full agent-facing instructions (also relevant to Gemini and other tools). It is the canonical copy; keep this file in sync with it rather than duplicating. The short version:

## Before committing

```
task precommit
```

Runs `gofmt` check, `markdownfmt` check, `go vet`, a build, `golangci-lint`, the `api.txt` drift check, and the race-enabled test suite. This is the same gate CI runs. Fix failures and rerun before committing; don't commit past a red gate. One-time setup to run this automatically on `git commit`: `task hooks:install`.

`docs/guide.md` covers the shape of the code and how to use it. `docs/design.md` explains why it is shaped that way. Read the relevant part of each before changing something non-trivial.
