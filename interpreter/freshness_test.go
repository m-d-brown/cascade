package interpreter

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func producesFreshFor(t *testing.T, a Action) bool {
	t.Helper()
	ok, err := inRun(t, work.Options{}, func(ctx *work.Context) (bool, error) {
		return producesFresh(ctx, world.Real(), a)
	})
	if err != nil {
		t.Fatalf("producesFresh: %v", err)
	}
	return ok
}

func TestProducesFreshMissingOutputIsStale(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "src"), "x")
	if producesFreshFor(t, Action{Dir: dir, Produces: []string{"out"}, Sources: []string{"src"}}) {
		t.Fatal("a missing output must be stale")
	}
}

func TestProducesFreshOutputNewerThanSourceIsFresh(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "src"), "x")
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(dir, "out"), "y")
	if !producesFreshFor(t, Action{Dir: dir, Produces: []string{"out"}, Sources: []string{"src"}}) {
		t.Fatal("output newer than source must be fresh")
	}
}

func TestProducesFreshSourceNewerThanOutputIsStale(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "out"), "y")
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(dir, "src"), "x")
	if producesFreshFor(t, Action{Dir: dir, Produces: []string{"out"}, Sources: []string{"src"}}) {
		t.Fatal("source newer than output must be stale")
	}
}

func TestProducesFreshDoubleStarSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "out"), "y")
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(dir, "pkg", "sub", "deep.go"), "package sub")
	if producesFreshFor(t, Action{Dir: dir, Produces: []string{"out"}, Sources: []string{"**/*.go"}}) {
		t.Fatal("a deep source file newer than the output must be stale")
	}
}

func TestProducesFreshExpandsHomeTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write(t, filepath.Join(home, "src"), "x")
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(home, "out"), "y")
	if !producesFreshFor(t, Action{Produces: []string{"~/out"}, Sources: []string{"~/src"}}) {
		t.Fatal("~ should expand the way the shell would for the command")
	}
}

func TestUnlessFresh(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cmd   string
		fresh bool
	}{
		{"exit zero is fresh", "true", true},
		{"exit non-zero is stale", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := inRun(t, work.Options{}, func(ctx *work.Context) (bool, error) {
				return unlessFresh(ctx, world.Real(), Action{Unless: tc.cmd})
			})
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.fresh {
				t.Fatalf("unlessFresh(%q) = %v, want %v", tc.cmd, ok, tc.fresh)
			}
		})
	}
}

func TestFreshNeedsASignalAndAllOfThem(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "out"), "y") // produces-fresh

	// no signal at all: never fresh
	if skip, _, _ := freshFor(t, Action{}, false); skip {
		t.Fatal("an action with no freshness signal is never fresh")
	}
	// produces says fresh, but every says stale: not skipped
	a := Action{Dir: dir, Produces: []string{"out"}, Every: "1h"}
	if skip, _, _ := freshFor(t, a, false); skip {
		t.Fatal("all signals must agree before an action is skipped")
	}
	// both agree
	if skip, _, _ := freshFor(t, a, true); !skip {
		t.Fatal("with every fresh and produces fresh, the action is up to date")
	}
}

func freshFor(t *testing.T, a Action, everyFresh bool) (bool, string, error) {
	t.Helper()
	type r struct {
		skip bool
		why  string
	}
	got, err := inRun(t, work.Options{}, func(ctx *work.Context) (r, error) {
		skip, why, err := fresh(ctx, world.Real(), a, everyFresh)
		return r{skip, why}, err
	})
	return got.skip, got.why, err
}
