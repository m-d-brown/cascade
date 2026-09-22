// Command snapshot backs a directory up once a day.
//
// It is the smallest complete example of the thing that makes a workflow
// like this different from a script that just does the work: whether to do
// the work at all is answered *before* anything expensive runs, in ordinary
// Go control flow. No step has to check a flag for it.
//
//	func snapshot(ctx *work.Context, w world.World) (string, error) {
//	    if v, meta, ok := work.LastRecord[string](ctx, "snapshot", work.Config(cfg)); ok && meta.Age() < every {
//	        return v, nil   // archive and collect are never called at all
//	    }
//	    return work.Do(ctx, "snapshot", func(ctx *work.Context) (string, error) {
//	        return takeSnapshot(ctx, w)
//	    }, work.Config(cfg))
//	}
//
// Run it twice:
//
//	snapshot run           # first time: collect, archive, snapshot, report
//	snapshot run           # again: report only, nothing else is even called
//	snapshot run --dry-run # walk the whole thing, touching nothing
//	snapshot state         # what the journal remembers
//	snapshot dot           # the shape of the run just made, as a picture
//
// The second run does not collect the files and then decide the snapshot is
// current: it never collects them, because the code above never reaches the
// call that would. That is what "back this up at most once a day" has to
// mean to be worth anything. The expensive part is collecting the files,
// and the only way not to pay for it is to never ask for it.
//
// Nothing here needs tar, a network or a daemon. Every effect on the
// outside world goes through a [world.World] ([world.Change],
// [world.Perform]) that main builds once and passes down, so none of the
// functions below has to check a `--dry-run` flag themselves; they read
// the same either way. That world is this program's own `--dry-run` flag,
// distinct from the framework's `--plan`: `--plan` (and `plan`, which is `run
// --plan`) only marks the run so it is never resumed from. See
// [github.com/m-d-brown/cascade/world] for why touching nothing is
// something an app opts into, not something the framework can decide on a
// call's behalf.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/m-d-brown/cascade/cli"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
	"github.com/spf13/pflag"
)

func main() {
	cli.Main(cli.App{
		Name:    "snapshot",
		Short:   "back up a directory, at most once a day",
		Version: "0.1.0",
		Long: `Back up a directory, at most once a day.

The workflow is report, which calls snapshot, which calls archive, which
calls collect. Only snapshot has a rule about whether it needs to run; because
nothing downstream of that decision is ever called unless the decision says
yes, that one rule decides whether any of the rest happens at all.`,
		DefaultStatePath: cli.UserStatePath("snapshot"),
		DefaultLogDir:    filepath.Join(os.TempDir(), "snapshot-logs"),

		Flags: flags,
		Flow: func(ctx *work.Context) (string, error) {
			w := world.Real()
			if dryRun {
				w = world.DryRun()
			}
			return work.Do(ctx, "report", func(ctx *work.Context) (string, error) {
				return report(ctx, w)
			})
		},
	})
}

// report says where the backup is. It has no rule of its own: it always
// calls snapshot, and reports whatever snapshot returns, whether that is a
// backup taken just now or one from yesterday afternoon.
func report(ctx *work.Context, w world.World) (string, error) {
	at, err := snapshot(ctx, w)
	if err != nil {
		return "", err
	}
	ctx.Summarize("backup is at %s", at)
	return at, nil
}

// snapshotConfig is what a recorded snapshot's fingerprint has to match for
// it to still count: change either and yesterday's answer no longer applies.
func snapshotConfig() string { return dest + "|" + every.String() }

// snapshot copies today's archive into the backup directory, unless there
// is already one from within the last day, in which case it returns that
// one and calls nothing else.
//
// [work.LastRecord] is asked before [takeSnapshot] is even named, because
// the point of asking is to decide whether to name it at all. It looks at
// what the journal remembers, because "when did I last do this?" is a
// question about this machine's history. A check can just as well ask the
// world instead (is the file already in the bucket?) when that machine's
// own history isn't the question being asked.
func snapshot(ctx *work.Context, w world.World) (string, error) {
	cfg := snapshotConfig()
	if last, meta, ok := work.LastRecord[string](ctx, "snapshot", work.Config(cfg)); ok && meta.HasValue {
		if age := meta.Age(); age < every {
			ctx.Logf("backed up %s ago, which is inside the %s limit", round(age), every)
			return last, nil
		}
	}
	return work.Do(ctx, "snapshot", func(ctx *work.Context) (string, error) {
		return takeSnapshot(ctx, w)
	}, work.Config(cfg))
}

// takeSnapshot is snapshot's call: it only ever runs when snapshot has
// already decided the work is needed.
func takeSnapshot(ctx *work.Context, w world.World) (string, error) {
	archive, err := work.Do(ctx, "archive", func(ctx *work.Context) (string, error) {
		return buildArchive(ctx, w)
	})
	if err != nil {
		return "", err
	}
	out := filepath.Join(dest, "backup-"+time.Now().Format("2006-01-02T15-04-05")+".txt")
	if err := world.Change(ctx, w, "copy "+archive+" to "+out, func() error {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(archive)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	}); err != nil {
		return "", err
	}
	ctx.Summarize("backed up to %s", out)
	return out, nil
}

// buildArchive writes the collected list to one file. It stands in for tar,
// and for everything else that is cheap to describe and expensive to do.
func buildArchive(ctx *work.Context, w world.World) (string, error) {
	listing, err := work.Do(ctx, "collect", func(ctx *work.Context) (string, error) {
		return collect(ctx, w)
	})
	if err != nil {
		return "", err
	}
	out := filepath.Join(os.TempDir(), "snapshot-archive.txt")
	ctx.Logf("packing %d bytes of listing", len(listing))
	if err := pause(ctx, 700*time.Millisecond); err != nil {
		return "", err
	}
	if err := world.Change(ctx, w, "write "+out, func() error {
		return os.WriteFile(out, []byte(listing), 0o644)
	}); err != nil {
		return "", err
	}
	ctx.Summarize("packed %d files", strings.Count(listing, "\n")+1)
	return out, nil
}

// collect walks the source tree. This is the expensive call: the one a
// daily backup must not pay for on the twenty-three hours it has nothing to
// do, which [snapshot] is what guarantees.
//
// Reading the disk goes through w like any other effect, so a dry-run world
// gets the stand-in and never depends on what happens to be on this
// machine.
func collect(ctx *work.Context, w world.World) (string, error) {
	ctx.Statusf("walking %s", source)
	if err := pause(ctx, 1500*time.Millisecond); err != nil {
		return "", err
	}
	names, err := world.Perform(ctx, w, world.Effect[[]string]{
		What:    "list the files under " + source,
		Do:      func() ([]string, error) { return list(source) },
		Instead: []string{"notes.md", "photos/spring.jpg", "src/main.go"},
	})
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("nothing to back up under %s", source)
	}
	ctx.Logf("found %d files", len(names))
	ctx.Summarize("collected %d files", len(names))
	return strings.Join(names, "\n"), nil
}

// ── Flags and helpers ────────────────────────────────────────────────────

var (
	source = "."
	dest   = filepath.Join(os.TempDir(), "snapshot-backups")
	every  = 24 * time.Hour
	dryRun bool
)

func flags(fs *pflag.FlagSet) {
	fs.StringVar(&source, "source", source, "directory to back up")
	fs.StringVar(&dest, "dest", dest, "directory the backups are written to")
	fs.DurationVar(&every, "every", every, "how often a backup is needed (try --every 1m)")
	fs.BoolVar(&dryRun, "dry-run", false, "let no effect reach the world: every one is logged as \"would …\" instead")
}

// list returns the paths under root, relative to it.
func list(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		// path != root: root's own name is "." for a relative root, which
		// would otherwise match the hidden-directory check below and skip
		// everything before the walk visits a single file.
		case path != root && d.IsDir() && strings.HasPrefix(d.Name(), "."):
			return filepath.SkipDir
		case d.IsDir():
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// pause is where this file would do real work; ctx is the context, so
// waiting on it is how a call notices the run being canceled.
func pause(ctx *work.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func round(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}
