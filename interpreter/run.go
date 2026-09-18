package interpreter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/mdbrown/cascade/units"
	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
)

// Run executes the cascade as a workflow: one work.Go call per
// action, wave by wave in dependency order, each command shelled out through
// w. A nil w is [world.Real].
//
// Each action reports the one-line summary its command last printed — a
// string, so the run journal can hand it back on --continue and the `every`
// check can read its age. An action that was already up to date, was blocked
// by a failed dependency, or is a bare barrier reports nothing and shows as
// skipped.
//
// Run has the shape [work.Do] wants, so it drops straight into a cli.App:
//
//	Flow: func(ctx *work.Context) (string, error) {
//	    r, err := interpreter.Load(path)
//	    if err != nil {
//	        return "", work.Fatal(err)
//	    }
//	    return r.Run(ctx, world.Real())
//	}
//
// The world is the caller's to build — Real, DryRun, or either wrapped in
// world.Confirm — the same way any workflow owns its own world.
func (r *Plan) Run(ctx *work.Context, w world.World) (string, error) {
	if w == nil {
		w = world.Real()
	}

	for _, warn := range r.warnings {
		ctx.Warnf("%s", warn)
	}

	fps := make(map[string]string, len(r.actions))
	for name, a := range r.actions {
		fps[name] = fingerprint(a)
	}

	everyFresh, err := r.checkEvery(ctx, w, fps)
	if err != nil {
		return "", err
	}

	futures := make(map[string]*work.Future[string], len(r.actions))
	unusable := make(map[string]bool)

	opts := func(a Action, fp string) []work.Option {
		o := []work.Option{work.Doc(a.Description), work.Config(fp), work.Tag("action")}
		if d, err := time.ParseDuration(a.Timeout); err == nil && d > 0 {
			o = append(o, work.Timeout(d))
		}
		return o
	}

	for _, wave := range r.waves {
		for _, name := range wave {
			name, a := name, r.actions[name]
			if dep, blocked := firstUnusableDep(a, unusable); blocked {
				futures[name] = work.Go(ctx, name, func(*work.Context) (string, error) {
					return "", work.Skip("blocked: %q did not succeed", dep)
				}, opts(a, fps[name])...)
				unusable[name] = true
				continue
			}
			futures[name] = work.Go(ctx, name, func(ctx *work.Context) (string, error) {
				return runAction(ctx, w, a, everyFresh[name])
			}, opts(a, fps[name])...)
		}
		for _, name := range wave {
			if _, err := futures[name].Get(); err != nil {
				unusable[name] = true
			}
		}
	}

	var ran, idle, failed int
	for _, name := range r.order {
		v, err := futures[name].Get()
		switch {
		case err != nil:
			failed++ // an optional action never returns an error, so this is a real failure
		case v == "":
			idle++
		default:
			ran++
		}
	}
	if failed > 0 {
		return "", fmt.Errorf("%d of %d actions failed", failed, len(r.order))
	}
	ctx.Summarize("%d actions · %d ran · %d unchanged", len(r.order), ran, idle)
	return fmt.Sprintf("%s: %d ran, %d unchanged", r.name, ran, idle), nil
}

// checkEvery answers the `every` journal-age question for each action that
// asks it. It runs on ctx, not the action's own context: work.LastRecord
// keys off ctx.Path(), which inside the action closure would already include
// the action's name. Routed through w so a dry-run world reports "not fresh"
// and `plan --dry-run` shows the whole cascade.
func (r *Plan) checkEvery(ctx *work.Context, w world.World, fps map[string]string) (map[string]bool, error) {
	out := make(map[string]bool, len(r.actions))
	for _, name := range r.order {
		a := r.actions[name]
		if a.Every == "" {
			continue
		}
		within, _ := time.ParseDuration(a.Every) // validated in build
		name, fp := name, fps[name]
		ok, err := world.Perform(ctx, w, world.Effect[bool]{
			What:    fmt.Sprintf("check whether %q succeeded in the last %s", name, a.Every),
			Instead: false,
			Do: func() (bool, error) {
				_, meta, ok := work.LastRecord[string](ctx, name, work.Config(fp))
				return ok && meta.Age() < within, nil
			},
		})
		if err != nil {
			return nil, err
		}
		out[name] = ok
	}
	return out, nil
}

// runAction is one action: check whether it is already up to date, and if
// not, run its command. It returns the command's last line of output, or ""
// when nothing was run.
func runAction(ctx *work.Context, w world.World, a Action, everyFresh bool) (string, error) {
	if skip, why, err := fresh(ctx, w, a, everyFresh); err != nil {
		return "", err
	} else if skip {
		return "", work.Skip("%s", why)
	}

	if strings.TrimSpace(a.Run) == "" {
		ctx.Summarize("barrier")
		return "", nil
	}

	if a.Dir != "" {
		if fi, err := os.Stat(expandHome(a.Dir)); err != nil || !fi.IsDir() {
			err := fmt.Errorf("working directory %q does not exist or is not a directory", a.Dir)
			if a.Optional {
				return "", work.Skip("optional: %v", err)
			}
			return "", err
		}
	}

	// While the command runs, keep the action's live status current: its own
	// latest line of output, or — if the action gave a progress: command —
	// that command's output, polled on an interval. Either way it reaches the
	// live tree and `cascade status` as a task-status event.
	watch := units.NewWatcher()
	stop := ctx.Monitor(progressInterval(a), progressProbe(a, watch))
	defer stop()

	res, err := units.Run(ctx, w, units.Cmd{
		Shell:     a.Run,
		Dir:       expandHome(a.Dir),
		Env:       envFor(a),
		AllowExit: a.AllowExit,
		Watch:     watch,
	})
	if err != nil {
		if a.Optional {
			return "", work.Skip("optional, and it failed: %v", err)
		}
		return "", err
	}

	if !res.DryRun {
		warnMissingOutputs(ctx, a)
	}
	summary := lastLine(res)
	ctx.Summarize("%s", summary)
	return summary, nil
}

// warnMissingOutputs flags a produces path the command did not actually
// create — almost always a typo in the path, and the reason the action would
// otherwise re-run on every invocation.
func warnMissingOutputs(ctx *work.Context, a Action) {
	for _, out := range a.Produces {
		p := joinDir(a.Dir, out)
		fi, err := os.Stat(p)
		switch {
		case err != nil:
			ctx.Warnf("ran, but did not create the declared output %q — freshness will re-run this every time", out)
		case fi.IsDir():
			ctx.Warnf("produces %q is a directory; freshness uses its own mtime, which a change to a file inside it does not update", out)
		}
	}
}

// minInterval floors a user-set progress-every, so a typo like "1ms" cannot
// spin the status probe (a subprocess, with a progress: command) in a hot loop.
const minInterval = time.Second

// progressInterval is how often to refresh a running action's status.
func progressInterval(a Action) time.Duration {
	if a.ProgressEvery != "" {
		if d, err := time.ParseDuration(a.ProgressEvery); err == nil && d > 0 {
			return max(d, minInterval)
		}
	}
	if a.Progress != "" {
		return 15 * time.Second
	}
	return 2 * time.Second
}

// progressProbe is the function ctx.Monitor calls each interval to produce
// the action's live status line.
func progressProbe(a Action, watch *units.Watcher) func(context.Context) (string, error) {
	if strings.TrimSpace(a.Progress) == "" {
		return func(context.Context) (string, error) { return watch.TailLine(1), nil }
	}
	// A progress command that outlives its own poll interval is misconfigured;
	// bound it so a hung one cannot stall the status monitor (and, through it,
	// the action finishing).
	timeout := min(progressInterval(a), 30*time.Second)
	dir, env := expandHome(a.Dir), envFor(a)
	return func(pctx context.Context) (string, error) {
		cctx, cancel := context.WithTimeout(pctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(cctx, "sh", "-c", a.Progress)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return lastTextLine(string(out)), nil
	}
}

// lastTextLine is the last non-empty line of s.
func lastTextLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// firstUnusableDep returns the first of a's needs that failed or is itself
// blocked, so the whole downstream of a failure is recorded as blocked.
func firstUnusableDep(a Action, unusable map[string]bool) (string, bool) {
	deps := append([]string(nil), a.Needs...)
	sort.Strings(deps)
	for _, d := range deps {
		if unusable[d] {
			return d, true
		}
	}
	return "", false
}

// lastLine is the last non-empty line the command printed, for the call's
// one-line summary.
func lastLine(res units.ExecResult) string {
	for i := len(res.Lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(res.Lines[i]); s != "" {
			return s
		}
	}
	if res.DryRun {
		return "would run"
	}
	return "ok"
}

// fingerprint is what a recorded result has to match for --continue and the
// `every` check to still trust it: everything about the action that changes
// what its command does. Freshness settings are deliberately left out —
// tightening `every` should not by itself force a re-run.
func fingerprint(a Action) string {
	parts := []string{a.Run, a.Dir}
	keys := make([]string, 0, len(a.Env))
	for k := range a.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+a.Env[k])
	}
	needs := append([]string(nil), a.Needs...)
	sort.Strings(needs)
	parts = append(parts, "needs:"+strings.Join(needs, ","))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}
