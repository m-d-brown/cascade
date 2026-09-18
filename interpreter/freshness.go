package interpreter

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
)

// fresh reports whether an action can be skipped: every freshness signal it
// configured agrees that it is up to date. An action that configures no
// signal is never fresh. everyFresh is the journal-age answer, computed by
// [Plan.Run] on the parent context because [work.LastRecord] keys off
// ctx.Path() and inside the action's own closure that path already carries
// the action's name.
func fresh(ctx *work.Context, w world.World, a Action, everyFresh bool) (skip bool, why string, err error) {
	signals := 0

	if a.Every != "" {
		signals++
		if !everyFresh {
			return false, "", nil
		}
	}
	if len(a.Produces) > 0 {
		signals++
		ok, err := producesFresh(ctx, w, a)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, "", nil
		}
	}
	if a.Unless != "" {
		signals++
		ok, err := unlessFresh(ctx, w, a)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, "", nil
		}
	}

	if signals == 0 {
		return false, "", nil
	}
	return true, "up to date", nil
}

// producesFresh is the produces/sources check: every output exists and is at
// least as new as the newest matched source.
func producesFresh(ctx *work.Context, w world.World, a Action) (bool, error) {
	return world.Perform(ctx, w, world.Effect[bool]{
		What:    "check whether " + strings.Join(a.Produces, ", ") + " is up to date",
		Instead: false, // a dry-run world cannot know: treat as stale
		Do: func() (bool, error) {
			newest, unmatched, err := newestSource(a.Dir, a.Sources)
			if err != nil {
				return false, err
			}
			for _, p := range a.Produces {
				fi, err := os.Stat(joinDir(a.Dir, p))
				if err != nil {
					return false, nil // missing output ⇒ stale
				}
				if !newest.IsZero() && fi.ModTime().Before(newest) {
					return false, nil // an input is newer ⇒ stale
				}
			}
			// The outputs are all present and current — but if a sources
			// pattern matched nothing, the check is blind to those inputs.
			for _, pat := range unmatched {
				ctx.Warnf("looks up to date, but the sources pattern %q matched no files, so a change to those inputs will not be noticed", pat)
			}
			return true, nil
		},
	})
}

// unlessFresh runs the unless command; exit 0 means the action is already
// done. Anything else — a clean non-zero exit or a command that could not run
// at all — is taken as stale, so a broken check never blocks the real work.
func unlessFresh(ctx *work.Context, w world.World, a Action) (bool, error) {
	return world.Perform(ctx, w, world.Effect[bool]{
		What:    "check: " + a.Unless,
		Instead: false, // a dry-run world does not run it: treat as stale
		Do: func() (bool, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", a.Unless)
			cmd.Dir = expandHome(a.Dir)
			cmd.Env = envFor(a)
			out, err := cmd.CombinedOutput()
			for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
				if line != "" {
					ctx.Logf("  %s", line)
				}
			}
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				return true, nil
			case errors.As(err, &exitErr):
				if code := exitErr.ExitCode(); code == 126 || code == 127 {
					ctx.Warnf("unless check exited %d (command not found or not executable) — treating the action as stale", code)
				}
				return false, nil // a non-zero exit: not done yet
			default:
				ctx.Warnf("unless check could not run (%v) — treating the action as stale", err)
				return false, nil
			}
		},
	})
}

// newestSource is the modification time of the newest file matched by any of
// the patterns, and the patterns that matched nothing at all.
func newestSource(dir string, patterns []string) (newest time.Time, unmatched []string, err error) {
	consider := func(path string) bool {
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() {
			return false
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return true
	}
	for _, pat := range patterns {
		matched := false
		full := joinDir(dir, pat)
		if !strings.Contains(pat, "**") {
			matches, err := filepath.Glob(full)
			if err != nil {
				return time.Time{}, nil, err
			}
			for _, m := range matches {
				if consider(m) {
					matched = true
				}
			}
			if !matched {
				unmatched = append(unmatched, pat)
			}
			continue
		}
		// "**" — walk from the static prefix, match the tail against each
		// file's name.
		i := strings.Index(full, "**")
		base := full[:i]
		if s := strings.LastIndexByte(base, '/'); s >= 0 {
			base = base[:s+1]
		}
		if base == "" {
			base = "."
		}
		tail := strings.Trim(full[i+len("**"):], "/")
		walkErr := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			switch {
			case err != nil && os.IsNotExist(err):
				return filepath.SkipDir // the pattern's root does not exist yet
			case err != nil:
				return err
			case d.IsDir():
				return nil
			}
			if hit, _ := filepath.Match(tail, filepath.Base(p)); (tail == "" || hit) && consider(p) {
				matched = true
			}
			return nil
		})
		if walkErr != nil {
			return time.Time{}, nil, walkErr
		}
		if !matched {
			unmatched = append(unmatched, pat)
		}
	}
	return newest, unmatched, nil
}

// joinDir resolves a cascade path against an action's Dir, expanding a
// leading ~ in either the way the shell would for the command itself.
func joinDir(dir, p string) string {
	p = expandHome(p)
	if filepath.IsAbs(p) {
		return p
	}
	if dir = expandHome(dir); dir == "" {
		return p
	}
	return filepath.Join(dir, p)
}

// expandHome expands a leading "~" or "~/" to the user's home directory —
// which "sh -c" does for Run but os.Stat and filepath.Glob do not.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// envFor is the environment for an action's command: the runner's own, plus
// the action's overrides. It returns nil when there are no overrides, so the
// child simply inherits.
func envFor(a Action) []string {
	if len(a.Env) == 0 {
		return nil
	}
	env := os.Environ()
	keys := make([]string, 0, len(a.Env))
	for k := range a.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+a.Env[k])
	}
	return env
}
