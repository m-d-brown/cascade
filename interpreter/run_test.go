package interpreter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/work"
	"github.com/mdbrown/cascade/world"
)

func TestRunExecutesInDependencyOrder(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, `
name: chain
actions:
  a: {run: 'echo a >> `+dir+`/log'}
  b: {run: 'echo b >> `+dir+`/log', needs: [a]}
  c: {run: 'echo c >> `+dir+`/log', needs: [b]}
`)
	res := runCascade(t, r, world.Real(), work.Options{})
	if res.ExitCode() != 0 {
		t.Fatalf("exit %d: %v", res.ExitCode(), res.Err())
	}
	got, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(string(got)), "") != "abc" {
		t.Fatalf("log = %q, want a b c in order", got)
	}
}

func TestRunSecondRunSkipsFreshActions(t *testing.T) {
	dir := t.TempDir()
	store := history.NewMemoryStore()
	r := mustParse(t, `
actions:
  compile:
    run: 'echo built > `+dir+`/app'
    produces: ['`+dir+`/app']
`)

	first := runCascade(t, r, world.Real(), work.Options{Store: store})
	if n := first.Counts[history.Succeeded]; n < 2 { // root + compile
		t.Fatalf("first run: %d succeeded, want the compile to run", n)
	}

	// Make the run's binary fingerprint stable between the two runs by
	// reusing the same store; the output now exists and is newer than any
	// (absent) source, so compile is fresh.
	second := runCascade(t, r, world.Real(), work.Options{Store: store})
	if second.Counts[history.Skipped] == 0 {
		t.Fatalf("second run skipped nothing: %v", second.Counts)
	}
	if task := second.Tasks[testPath(second, "compile")]; task == nil || task.Status != history.Skipped {
		t.Fatalf("second run: compile status = %v, want skipped", statusOf(task))
	}
}

func TestRunBlocksDependentsOfAFailureButNotIndependentWork(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, `
actions:
  broken: {run: 'exit 3'}
  after:  {run: 'echo after > `+dir+`/after', needs: [broken]}
  alone:  {run: 'echo alone > `+dir+`/alone'}
`)
	res := runCascade(t, r, world.Real(), work.Options{})

	if res.ExitCode() == 0 {
		t.Fatal("a failed action must make the run fail")
	}
	if broken := res.Tasks[testPath(res, "broken")]; statusOf(broken) != history.Failed {
		t.Fatalf("broken status = %v, want failed", statusOf(broken))
	}
	if after := res.Tasks[testPath(res, "after")]; statusOf(after) != history.Skipped {
		t.Fatalf("after status = %v, want skipped (blocked)", statusOf(after))
	}
	if _, err := os.Stat(filepath.Join(dir, "after")); err == nil {
		t.Fatal("the blocked action's command must not have run")
	}
	if _, err := os.Stat(filepath.Join(dir, "alone")); err != nil {
		t.Fatal("independent work must still run after an unrelated failure")
	}
}

func TestRunEverySkipsWithinTheWindow(t *testing.T) {
	dir := t.TempDir()
	store := history.NewMemoryStore()
	r := mustParse(t, `
actions:
  snapshot:
    run: 'date >> `+dir+`/runs'
    every: 1h
`)

	runCascade(t, r, world.Real(), work.Options{Store: store})
	second := runCascade(t, r, world.Real(), work.Options{Store: store})

	if task := second.Tasks[testPath(second, "snapshot")]; statusOf(task) != history.Skipped {
		t.Fatalf("second run within the window: snapshot = %v, want skipped", statusOf(task))
	}
	got, err := os.ReadFile(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(got)), "\n") + 1; lines != 1 {
		t.Fatalf("command ran %d times, want 1", lines)
	}
}

func TestRunWarnsWhenAnActionSkipsItsDeclaredOutput(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, `
actions:
  typo:
    run: 'echo hi > `+dir+`/real.txt'
    produces: ['`+dir+`/typo.txt']
`)
	warnings := runWithWarnings(t, r, world.Real())
	if !hasWarning(warnings, "did not create the declared output") {
		t.Fatalf("no missing-output warning in:\n%s", strings.Join(warnings, "\n"))
	}
}

func TestRunWarnsWhenSourcesPatternMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := mustParse(t, `
actions:
  a:
    run: 'true'
    produces: ['`+dir+`/out']
    sources: ['`+dir+`/nope/**/*.go']
`)
	warnings := runWithWarnings(t, r, world.Real())
	if !hasWarning(warnings, "matched no files") {
		t.Fatalf("no blind-sources warning in:\n%s", strings.Join(warnings, "\n"))
	}
}

func TestRunTimeoutKillsALongAction(t *testing.T) {
	r := mustParse(t, "actions:\n  slow: {run: 'sleep 10', timeout: 200ms}\n")
	res := runCascade(t, r, world.Real(), work.Options{})
	tr := res.Tasks[testPath(res, "slow")]
	if statusOf(tr) != history.Failed || !strings.Contains(tr.Err.Error(), "timed out") {
		t.Fatalf("want a timeout failure, got %v / %v", statusOf(tr), tr.Err)
	}
}

func TestRunClearErrorForMissingWorkingDir(t *testing.T) {
	r := mustParse(t, "actions:\n  a: {run: 'true', dir: /no/such/dir/at/all}\n")
	res := runCascade(t, r, world.Real(), work.Options{})
	tr := res.Tasks[testPath(res, "a")]
	if statusOf(tr) != history.Failed || !strings.Contains(tr.Err.Error(), "working directory") {
		t.Fatalf("want a clear 'working directory' failure, got %v / %v", statusOf(tr), tr.Err)
	}
}

func TestRunUnlessCommandNotFoundRunsTheActionAndWarns(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, `
actions:
  a:
    run: 'echo ran > `+dir+`/marker'
    unless: this-cmd-truly-does-not-exist
`)
	warnings := runWithWarnings(t, r, world.Real())
	if !hasWarning(warnings, "command not found") {
		t.Fatalf("no unless-not-found warning in:\n%s", strings.Join(warnings, "\n"))
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Fatal("action should have run despite the broken unless check")
	}
}

func TestRunOptionalFailureDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, `
actions:
  maybe: {run: 'exit 1', optional: true}
  after: {run: 'echo ok > `+dir+`/after', needs: [maybe]}
`)
	res := runCascade(t, r, world.Real(), work.Options{})
	if res.ExitCode() != 0 {
		t.Fatalf("an optional failure must not fail the run: %v", res.Err())
	}
	if _, err := os.Stat(filepath.Join(dir, "after")); err != nil {
		t.Fatal("an action after an optional failure must still run")
	}
}

func TestRunDryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	r := mustParse(t, `
actions:
  a: {run: 'echo a > `+dir+`/a'}
  b: {run: 'echo b > `+dir+`/b', needs: [a]}
`)
	res := runCascade(t, r, world.DryRun(), work.Options{Pretend: true})
	if res.ExitCode() != 0 {
		t.Fatalf("dry run failed: %v", res.Err())
	}
	for _, f := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("dry run created %s", f)
		}
	}
}

// testPath is the run-relative path of an action: the root call's name is the
// runner's Options.Name, defaulting to "run".
func testPath(res *history.Result, action string) string {
	root := "run"
	for _, p := range res.Order {
		if !strings.Contains(p, "/") {
			root = p
			break
		}
	}
	return root + "/" + action
}

func statusOf(tr *history.TaskResult) history.Status {
	if tr == nil {
		return history.Pending
	}
	return tr.Status
}
