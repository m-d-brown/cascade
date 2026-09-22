// Command release ships a new version of a make-believe program called
// widget: build it for three platforms, test it, sign it, publish it,
// announce it, and clean up afterwards.
//
// It doubles as a tour of the framework: every feature appears here
// somewhere, each with a comment explaining why. [examples/engine/minimal]
// is the smaller half of the tour, the smallest complete workflow there is,
// and worth reading first if this one feels like a lot at once.
//
// main names the workflow "release"; everything else is an ordinary Go
// function it calls, directly or through [work.Do] and [work.Go]. The file
// reads top to bottom as the pipeline it runs: there is no separate
// description of the pipeline to keep in sync with the code.
//
// Only [release] and [prepare] decide what this program does and in what
// order, so only they call [work.Do] or [work.Go]. Everything else, from
// [sign] to [build], is a plain function: it takes what it needs as
// parameters and returns a value and an error like any other Go function,
// with no idea that it's being named or tracked. Naming happens at the call
// site, which already knows the name. Sometimes that name is fixed:
//
//	signed, err := work.Do(ctx, "sign", func(ctx *work.Context) (string, error) {
//	    return sign(ctx, archive, s.signKey)
//	}, work.Tag("release"))
//
// and sometimes it depends on where the call happens, such as a loop
// building one call per platform:
//
//	work.Go(ctx, "build-"+goos, func(ctx *work.Context) (string, error) {
//	    return build(ctx, p, goos)
//	})
//
// A function that takes nothing but ctx needs no closure at all; [work.Do]
// takes it directly:
//
//	func pickVersion(ctx *work.Context) (string, error) { … }
//
//	version, err := work.Do(ctx, "version", pickVersion)
//
// Two functions decide for themselves whether they need to run, each
// checking a different source: [downloadModules] asks its world, through
// [world.Perform]; [writeNotes] asks the journal, through
// [work.LastRecord]. Both wrap their own [work.Do] internally, because the
// decision belongs right where the check is, not with whatever calls them.
// [examples/engine/minimal] is built entirely around this shape, with
// nothing else going on around it.
//
// [work.Do]'s first return value is what the caller gets back, as the real
// Go type it is: nothing to convert, nothing to decode. A call reads
// exactly what its caller passes it and nothing else, so there is no shared
// state, no context bag, and no registry for two calls to collide over.
// Call [work.Context.Summarize] to set the one-line summary the run shows
// for a call; leave it unset and a non-empty string result stands in for
// it.
//
// [work.Context] is also a [context.Context]: `exec.CommandContext(ctx, …)`
// and `<-ctx.Done()` cancel when the run does, whether that's from ^C, `q`
// in the live display, or a critical failure elsewhere in the run. See
// [testUnit], marked [work.Critical], and what canceling it does to
// [testRace], still running beside it.
//
// [announce] is its own small tour of a workflow with no single tree:
// independent tasks started with [work.Go] and joined with [errors.Join],
// the same way [build] handles the platforms, but with nothing gathering
// the results afterward beyond whether they all succeeded.
//
// Nothing here touches the network or needs anything installed beyond the
// Go toolchain already used to build it. [installToolchain] runs `go
// version` for real; everything else is simulated, even under --dry-run.
// All of this is safe to run:
//
//	release run                        # watch it run
//	release run --fail build-darwin    # see a failure, and what it takes down with it
//	release run --fail test-unit       # a Critical failure cancels test-race, still in flight
//	release run --no-race              # test-race reports work.Skip instead of running
//	release run --dry-run              # let no effect reach the world
//	release run --continue last        # finish an interrupted run
//	release dot | dot -Tsvg -o release.svg   # the shape of the run just made, as a picture
//
// release --dry-run and release plan (`run --plan`) are two different
// things on purpose. --dry-run is this program's own flag, built into a
// [world.World] that [release] passes down to whatever needs it. The
// framework itself has no such flag: only the app knows which of its own
// calls a dry run should reach (see
// [github.com/m-d-brown/cascade/world]). --plan, instead, only marks the
// run so it is never resumed from. Combine both, `plan --dry-run`, for a
// run that does neither.
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/m-d-brown/cascade/cli"
	"github.com/m-d-brown/cascade/units"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
	"github.com/spf13/pflag"
)

func main() {
	cli.Main(cli.App{
		Name:  "release",
		Short: "build, test, sign, publish and announce a release of widget",
		Long: `Ship a release of widget: the tour of every feature the framework has.

See the package doc (go doc ./examples/engine/complete) for how the file is put
together and what to try.`,
		Version: "0.1.0",

		// release is the whole of what a run does. It takes exactly the
		// shape Flow wants, the same shape work.Do itself takes, so there
		// is nothing to wrap it in.
		Flow: release,

		// What tells one run of this apart from another: the version being
		// released. Shown in `start`'s run list and `runs`, so picking
		// which past run to continue or read the log of does not mean
		// opening the journal to remember which one was which.
		Describe: func() string { return "v" + versionFlag },

		Flags: flags,

		// Left at their defaults, examples write state and logs into the
		// current directory; a real program usually wants them somewhere
		// stable instead.
		DefaultStatePath: cli.UserStatePath("release"),
		DefaultLogDir:    filepath.Join(os.TempDir(), "release-logs"),
	})
}

// platforms is what we build for.
var platforms = []string{"linux", "darwin", "windows"}

// release is what the program is for: widget is public, everyone has been
// told, and the build directory is gone, whether or not the rest went
// well.
//
// It runs directly on the ctx the workflow itself was handed, rather than
// wrapping its own body in another named [work.Do] call. [cli.App] already
// names the whole run after the program (here, "release"), so a second
// "release" nested inside it would only repeat the name for no reason. Its
// callees are exactly the calls a run's trace shows hanging off the root.
func release(ctx *work.Context) (string, error) {
	w := world.Real()
	if dryRun {
		w = world.DryRun()
	}

	p, err := work.Do(ctx, "prepare", func(ctx *work.Context) (prepared, error) {
		return prepare(ctx, w)
	}, work.Tag("prepare"), work.Doc("collect everything every platform's build needs, once"))
	if err != nil {
		return "", err
	}

	// Fanning out and back in is ordinary Go: the builds do not name each
	// other, so nothing serializes them. work.Go starts one per platform,
	// bounded only by --jobs, and Get blocks until each is done.
	building := make(map[string]*work.Future[string], len(platforms))
	for _, goos := range platforms {
		goos := goos
		building[goos] = work.Go(ctx, "build-"+goos, func(ctx *work.Context) (string, error) {
			return build(ctx, p, goos)
		}, work.Tag("build"))
	}
	binaries := make(map[string]string, len(platforms))
	for _, goos := range platforms {
		bin, err := building[goos].Get()
		if err != nil {
			return "", err
		}
		binaries[goos] = bin
	}
	linuxBinary := binaries["linux"]

	// Both test runs use the linux binary, and neither one names the
	// other, so they run side by side. test-unit is Critical: try
	// `--fail test-unit` and watch test-race, still running, get canceled
	// along with it rather than finish for nothing.
	unit := work.Go(ctx, "test-unit", func(ctx *work.Context) (int, error) {
		return testUnit(ctx, linuxBinary)
	}, work.Tag("test"), work.Critical())
	race := work.Go(ctx, "test-race", func(ctx *work.Context) (int, error) {
		return testRace(ctx, linuxBinary)
	}, work.Tag("test"))

	// publish must not happen until the tests have passed. It is a strict
	// gate, checked once and then simply not read again.
	if _, err := unit.Get(); err != nil {
		return "", err
	}
	// The race report makes the release notes better, but its absence (a
	// failure, a `--no-race` skip, or a run that never asked for it) must
	// not stop the release. raceErr is read for whether there is a count
	// to report, never to fail the run.
	races, raceErr := race.Get()

	archive, err := work.Do(ctx, "package", func(ctx *work.Context) (string, error) {
		return packageBinaries(ctx, binaries)
	}, work.Tag("release"))
	if err != nil {
		return "", err
	}

	s, err := readSecrets(ctx, w)
	if err != nil {
		return "", err
	}
	signed, err := work.Do(ctx, "sign", func(ctx *work.Context) (string, error) {
		return sign(ctx, archive, s.signKey)
	}, work.Tag("release"))
	if err != nil {
		return "", err
	}
	// writeNotes decides for itself whether the changelog for this version
	// already exists, so it is called directly rather than wrapped in
	// another work.Do. See the package doc.
	notes, err := writeNotes(ctx, p, races, raceErr == nil)
	if err != nil {
		return "", err
	}
	url, err := work.Do(ctx, "publish", func(ctx *work.Context) (string, error) {
		return publish(ctx, p, signed, notes, s.publishToken)
	}, work.Tag("release"), work.Timeout(2*time.Minute), work.Doc("upload the signed archives"))
	if err != nil {
		return "", err
	}

	// announce is a strict step: its failure fails the release. cleanup
	// runs whether or not it does. Try `run --fail announce-releases` and
	// watch the build directory still get removed.
	_, announceErr := work.Do(ctx, "announce", func(ctx *work.Context) (string, error) {
		return announce(ctx, url)
	}, work.Tag("notify"))
	if _, err := work.Do(ctx, "cleanup", cleanup, work.Tag("release")); err != nil {
		ctx.Warnf("cleanup failed: %v", err)
	}
	if announceErr != nil {
		return "", announceErr
	}
	ctx.Summarize("released %s", url)
	return url, nil
}

// announce tells people where the release is, on every channel at once. It
// is a small workflow of its own with no single tree (see [design
// decisions](../../../docs/design.md#dynamic-execution-over-declared-graphs)),
// so it starts one independent call per channel with work.Go and joins
// whatever failed. It's the same shape [build] takes for the platforms,
// without anything gathering the results back up afterward.
func announce(ctx *work.Context, url string) (string, error) {
	channels := []string{"#releases", "team@example.com"}
	futures := make([]*work.Future[string], len(channels))
	for i, channel := range channels {
		channel := channel
		futures[i] = work.Go(ctx, "announce-"+sanitize(channel), func(ctx *work.Context) (string, error) {
			return announceOn(ctx, url, channel)
		}, work.Tag("notify"))
	}
	var errs []error
	for _, f := range futures {
		if _, err := f.Get(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	ctx.Summarize("announced %s on %d channels", url, len(channels))
	return url, nil
}

// announceOn announces on one channel.
func announceOn(ctx *work.Context, url, channel string) (string, error) {
	if err := simulate(ctx, 200*time.Millisecond, "announcing "+url+" on "+channel); err != nil {
		return "", err
	}
	ctx.Summarize("announced on %s", channel)
	return channel, nil
}

// sanitize turns a channel name into something safe to use in a call's name.
func sanitize(channel string) string {
	return strings.NewReplacer("#", "", "@", "-at-", ".", "-").Replace(channel)
}

// cleanup removes the build directory. It is called after announce whether
// or not announce succeeded (see [release]), the same way a deferred
// cleanup runs however the work it followed turned out. It takes nothing
// but ctx, so [release] passes it to [work.Do] directly.
func cleanup(ctx *work.Context) (bool, error) {
	if err := simulate(ctx, 400*time.Millisecond, "rm -rf build/"); err != nil {
		return false, err
	}
	ctx.Summarize("build directory removed")
	return true, nil
}

// publish uploads the signed archives, with the release notes, once the
// tests have passed.
func publish(ctx *work.Context, p prepared, signed, notes, token string) (string, error) {
	ctx.Logf("uploading %s with %s", signed, notes)
	if token == "" {
		return "", fmt.Errorf("no publishing token")
	}

	// Long-running work reports progress without flooding the log: the
	// monitor publishes one status line every interval, and only the most
	// recent one is shown on this call's row. It runs on its own
	// goroutine, so what it reads has to be safe to share.
	var done atomic.Int64
	stop := ctx.Monitor(300*time.Millisecond, func(context.Context) (string, error) {
		return fmt.Sprintf("uploaded %d%%", done.Load()), nil
	})
	for pct := int64(0); pct < 100; pct += 12 {
		done.Store(pct)
		if err := nap(ctx, 200*time.Millisecond); err != nil {
			return "", err
		}
	}
	stop()

	// The call's own deadline, set by [release] with [work.Timeout]: past
	// it ctx is canceled and this call fails, which fails publish and
	// everything waiting on it, and leaves the rest of the run alone. nap
	// is what notices.
	url := "https://example.com/widget/v" + p.version
	ctx.Summarize("published %s", url)
	return url, nil
}

// sign signs the archives with the release key.
func sign(ctx *work.Context, archive, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty signing key")
	}
	if err := simulate(ctx, 600*time.Millisecond, "signing 3 archives"); err != nil {
		return "", err
	}
	ctx.Summarize("signed 3 archives")
	return archive + "SHA256SUMS.asc", nil
}

// packageBinaries archives every binary we built.
func packageBinaries(ctx *work.Context, binaries map[string]string) (string, error) {
	for _, goos := range platforms {
		ctx.Logf("archiving %s", binaries[goos])
	}
	if err := simulate(ctx, 800*time.Millisecond, "tar czf", "zip"); err != nil {
		return "", err
	}
	ctx.Summarize("packaged %d archives", len(binaries))
	return "dist/", nil
}

// build compiles widget for one platform.
func build(ctx *work.Context, p prepared, goos string) (string, error) {
	ctx.Logf("building %s against %s", p.generated, p.modules)
	if err := simulate(ctx, 1600*time.Millisecond, "compiling ./...", "linking", "stripping"); err != nil {
		return "", err
	}
	binary := fmt.Sprintf("dist/widget-%s-%s", p.version, goos)
	ctx.Summarize("%s (14.2 MiB)", binary)
	return binary, nil
}

// testUnit runs the tests against the linux binary. It is marked
// [work.Critical] where it is called from [release]: its failure cancels
// the whole run, test-race included, rather than only blocking whatever
// would have read its result.
func testUnit(ctx *work.Context, binary string) (int, error) {
	ctx.Logf("testing %s", binary)
	if err := simulate(ctx, 1200*time.Millisecond, "ok  acme/widget", "ok  acme/widget/store"); err != nil {
		return 0, err
	}
	ctx.Summarize("312 tests passed")
	return 312, nil
}

// testRace runs the same tests under the race detector, alongside them,
// unless --no-race disabled it. [Skip] is how a call reports a disabled
// feature rather than an error: it still counts as success, and is
// reported apart from an ordinary one.
func testRace(ctx *work.Context, binary string) (int, error) {
	if noRace {
		return 0, work.Skip("race detector disabled by --no-race")
	}
	if err := simulate(ctx, 2200*time.Millisecond, "ok  acme/widget", "ok  acme/widget/store"); err != nil {
		return 0, err
	}
	ctx.Summarize("no races detected")
	return 0, nil
}

// writeNotes writes the changelog for this version, unless it was already
// written by an earlier run. [work.LastRecord] is what checks that: the
// same question [downloadModules] asks, but of the journal instead of the
// world, since "was this version's changelog already written" is a
// question about this machine's history rather than about a file on disk.
// races is read only if raceReportOK says there is one to report, the same
// "read it if it's there" shape [work.Future.Get]'s second return value
// gives any optional result.
func writeNotes(ctx *work.Context, p prepared, races int, raceReportOK bool) (string, error) {
	if v, meta, ok := work.LastRecord[string](ctx, "notes", work.Config(p.version)); ok && meta.HasValue {
		ctx.Logf("changelog for v%s was already written %s ago", p.version, meta.Age().Round(time.Second))
		return v, nil
	}
	return work.Do(ctx, "notes", func(ctx *work.Context) (string, error) {
		if raceReportOK {
			ctx.Logf("race detector reported %d races", races)
		} else {
			ctx.Warnf("no race report; writing the notes without it")
		}
		ctx.Logf("reading %s", p.source)
		if err := simulate(ctx, 500*time.Millisecond, "collecting merged pull requests"); err != nil {
			return "", err
		}
		ctx.Summarize("wrote CHANGELOG for v%s", p.version)
		return "CHANGELOG.md", nil
	}, work.Tag("release"), work.Config(p.version))
}

// ── What the builds need ─────────────────────────────────────────────────

// prepared is what every build, test and the release notes need in common:
// the version, the fetched source, and what generation and the module cache
// produced from it. It is fetched once, by [prepare], and passed down as an
// ordinary value. That replaces what used to be several constructors
// sharing one node by sharing a name: here, the shared work runs once and
// hands its result to everything that needs it.
type prepared struct {
	version   string
	source    string
	generated string
	modules   string
}

// prepare settles the version, fetches the source, runs code generation,
// populates the module cache, and makes sure the compiler toolchain is
// installed: everything every platform's build needs, done once.
func prepare(ctx *work.Context, w world.World) (prepared, error) {
	version, err := work.Do(ctx, "version", pickVersion, work.Tag("prepare"), work.Config(versionFlag))
	if err != nil {
		return prepared{}, err
	}
	source, err := work.Do(ctx, "source", func(ctx *work.Context) (string, error) {
		return fetchSource(ctx, version)
	}, work.Tag("prepare"))
	if err != nil {
		return prepared{}, err
	}
	generated, err := work.Do(ctx, "generate", func(ctx *work.Context) (string, error) {
		return generate(ctx, source)
	}, work.Tag("prepare"))
	if err != nil {
		return prepared{}, err
	}
	// downloadModules decides for itself whether the cache is already
	// populated, so it is called directly rather than wrapped in another
	// work.Do. See the package doc.
	modules, err := downloadModules(ctx, w, source)
	if err != nil {
		return prepared{}, err
	}
	if _, err := work.Do(ctx, "toolchain", func(ctx *work.Context) (string, error) {
		return installToolchain(ctx, w)
	}, work.Tag("prepare")); err != nil {
		return prepared{}, err
	}
	ctx.Summarize("prepared v%s", version)
	return prepared{version, source, generated, modules}, nil
}

// generate runs code generation over the source tree, and hands on the
// directory it wrote to.
func generate(ctx *work.Context, source string) (string, error) {
	if err := simulate(ctx, 700*time.Millisecond, "stringer", "mockgen", "protoc"); err != nil {
		return "", err
	}
	ctx.Summarize("generated 12 files")
	return source + "/gen", nil
}

// downloadModules fetches the dependencies, unless they are already on
// disk, which is what the check below is for.
//
// The check goes through [world.Perform], the same as any other read of w:
// a dry-run world is told "no" and never depends on what happens to be on
// the machine running it, the way a raw os.Stat call would. It runs before
// the download itself is even named, because the point of the check is
// deciding whether to name it at all. Run this example twice with the same
// --cache-dir and watch the second run skip straight past it.
func downloadModules(ctx *work.Context, w world.World, source string) (string, error) {
	cached, err := world.Perform(ctx, w, world.Effect[bool]{
		What:    "check for a module cache at " + cacheDir,
		Do:      func() (bool, error) { _, err := os.Stat(cacheDir); return err == nil, nil },
		Instead: false,
	})
	if err != nil {
		return "", err
	}
	if cached {
		ctx.Logf("module cache already populated at %s", cacheDir)
		return cacheDir, nil
	}
	return work.Do(ctx, "modules", func(ctx *work.Context) (string, error) {
		if err := simulate(ctx, 1400*time.Millisecond, "downloading 84 modules", "verifying checksums"); err != nil {
			return "", err
		}
		// Everything that reaches outside the program goes through w, so
		// --dry-run needs nothing from this call: there is no branch here
		// on what kind of world it is.
		if err := world.Change(ctx, w, "create "+cacheDir, func() error {
			return os.MkdirAll(cacheDir, 0o755)
		}); err != nil {
			return "", err
		}
		ctx.Summarize("downloaded 84 modules")
		return cacheDir, nil
	}, work.Tag("prepare"), work.Config(cacheDir), work.Doc("populate the module cache"))
}

// installToolchain makes sure the compiler toolchain is the one this
// release expects. It's the one real external command in this file, run
// through [units.Run] so a dry-run w never actually runs it, with a
// [units.Watcher] feeding its output to [work.Context.Monitor] as it goes.
// Every build needs this, and calling it once from [prepare] rather than
// once per build is what makes it run once: there is no name-based folding
// to do that here.
func installToolchain(ctx *work.Context, w world.World) (string, error) {
	watch := units.NewWatcher()
	stop := ctx.Monitor(2*time.Second, func(context.Context) (string, error) {
		return watch.TailLine(1), nil
	})
	defer stop()
	res, err := units.Run(ctx, w, units.Cmd{
		Path: "go", Args: []string{"version"}, Quiet: true, Watch: watch,
		Instead: []string{"go version go1.26.0 linux/amd64"},
	})
	if err != nil {
		return "", err
	}
	ctx.Summarize("%s", res.Output())
	return "go1.26", nil
}

// fetchSource fetches the tree everything is built from.
func fetchSource(ctx *work.Context, version string) (string, error) {
	ctx.Logf("cloning git@example.com:acme/widget.git at v%s", version)
	if err := simulate(ctx, 900*time.Millisecond, "counting objects", "resolving deltas"); err != nil {
		return "", err
	}
	ctx.Summarize("fetched 4,128 files")
	return "/tmp/widget", nil
}

// pickVersion is where the workflow gets its input. It takes nothing but
// ctx, so [prepare] passes it to [work.Do] directly.
func pickVersion(ctx *work.Context) (string, error) {
	ctx.Summarize("releasing v%s", versionFlag)
	return versionFlag, nil
}

// secrets are the two this release needs. Both come from the same source,
// so [units.Secrets] reads them in one call and one prompt.
type secrets struct{ signKey, publishToken string }

// readSecrets fetches both secrets in one call. A missing signing key is
// fatal to the whole run: there is no point doing any of the rest without
// it. [work.Fatal] is what says so; nothing else here is.
func readSecrets(ctx *work.Context, w world.World) (secrets, error) {
	values, err := units.Secrets(ctx, w, envSecrets{}, "secrets", map[string]string{
		"sign-key":      "RELEASE_SIGN_KEY",
		"publish-token": "RELEASE_PUBLISH_TOKEN",
	})
	if err != nil {
		return secrets{}, work.Fatal(err)
	}
	return secrets{signKey: values["sign-key"], publishToken: values["publish-token"]}, nil
}

// envSecrets reads secrets from the environment. Any source can be plugged
// in here: 1Password, a keychain, a cloud secret manager. It reads
// directly rather than through w, since an environment variable belongs to
// this process, not the outside world. A source that shells out or calls a
// network vault would route that through w the same way [installToolchain]
// routes running a command through one.
type envSecrets struct{}

func (envSecrets) ID() string { return "the environment" }

func (envSecrets) Read(ctx *work.Context, _ world.World, refs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, ref := range refs {
		value := os.Getenv(ref)
		if value == "" {
			value = "placeholder-" + strings.ToLower(ref) // an example should still run
			ctx.Warnf("%s is not set, using a placeholder", ref)
		}
		out[ref] = value
	}
	return out, nil
}

// ── Flags and helpers ────────────────────────────────────────────────────

var (
	versionFlag = "1.4.0"
	failStep    string
	speed       = 1.0
	noRace      bool
	dryRun      bool
	cacheDir    = filepath.Join(os.TempDir(), "cascade-release-cache")
)

func flags(fs *pflag.FlagSet) {
	fs.StringVar(&versionFlag, "version-tag", versionFlag, "version being released")
	fs.StringVar(&failStep, "fail", "", "make this call fail, to see what it takes down with it")
	fs.Float64Var(&speed, "speed", speed, "multiply every call's duration (0.1 = ten times faster)")
	fs.BoolVar(&noRace, "no-race", false, "skip the race detector (work.Skip, not a failure)")
	fs.BoolVar(&dryRun, "dry-run", false, "let no effect reach the world: every one is logged as \"would …\" instead")
	fs.StringVar(&cacheDir, "cache-dir", cacheDir, "directory the module cache check looks for")
}

// simulate stands in for doing something: it reports progress as it goes,
// and fails if --fail named the call currently running.
func simulate(ctx *work.Context, d time.Duration, steps ...string) error {
	d = time.Duration(float64(d) * speed)
	for i, step := range steps {
		if err := nap(ctx, d/time.Duration(len(steps))); err != nil {
			return err
		}
		ctx.Statusf("%s (%d/%d)", step, i+1, len(steps))
	}
	if ctx.Name() == failStep {
		return fmt.Errorf("%s failed: exit status 2\n\tstderr: widget.go:42: undefined: doTheThing", ctx.Name())
	}
	return nil
}

// nap is where this file would do real work, and shows the one thing that
// work must get right: ctx is the context, so waiting on it is how a call
// notices the run being canceled.
func nap(ctx *work.Context, d time.Duration) error {
	jitter := time.Duration(rand.Int63n(int64(d/4 + 1)))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d + jitter):
		return nil
	}
}
