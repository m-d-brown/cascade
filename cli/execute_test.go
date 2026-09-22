package cli_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m-d-brown/cascade/cli"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
)

// captureExecute swaps os.Args, os.Stdin, os.Stdout and os.Stderr, drives the
// real cobra tree through cli.Execute, and returns everything written.
func captureExecute(t *testing.T, args []string, stdin string, app cli.App) (stdout, stderr string, code int) {
	t.Helper()

	oldArgs := os.Args
	os.Args = append([]string{"testapp"}, args...)
	t.Cleanup(func() { os.Args = oldArgs })

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = inR, outW, errW
	t.Cleanup(func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr })

	go func() {
		_, _ = inW.WriteString(stdin)
		_ = inW.Close()
	}()

	// Drain both pipes concurrently with Execute writing to them. A pipe's
	// buffer is bounded, so reading only after Execute returns would
	// deadlock the moment a test produces more output than that buffer
	// holds.
	var outBuf, errBuf bytes.Buffer
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go func() { _, _ = io.Copy(&outBuf, outR); close(outDone) }()
	go func() { _, _ = io.Copy(&errBuf, errR); close(errDone) }()

	code = cli.Execute(app)

	_ = outW.Close()
	_ = errW.Close()
	<-outDone
	<-errDone
	return outBuf.String(), errBuf.String(), code
}

func testApp(t *testing.T, fail bool) cli.App {
	dir := t.TempDir()
	return cli.App{
		Name: "testapp",
		Flow: func(ctx *work.Context) (string, error) {
			return work.Do(ctx, "build", func(ctx *work.Context) (string, error) {
				ctx.Logf("building")
				if fail {
					return "", errors.New("build broke")
				}
				ctx.Summarize("built")
				return "dist/x", nil
			})
		},
		Describe:         func() string { return "v1.0" },
		DefaultStatePath: filepath.Join(dir, "state.json"),
		DefaultLogDir:    filepath.Join(dir, "logs"),
	}
}

func TestExecuteRunSucceeds(t *testing.T) {
	out, _, code := captureExecute(t, []string{"run", "--plain"}, "", testApp(t, false))
	if code != 0 {
		t.Fatalf("code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "testapp/build") {
		t.Fatalf("output missing the task path:\n%s", out)
	}
}

func TestExecuteRunReportsFailureExitCode(t *testing.T) {
	out, _, code := captureExecute(t, []string{"run", "--plain"}, "", testApp(t, true))
	if code == 0 {
		t.Fatalf("code = 0, want nonzero; output:\n%s", out)
	}
}

func TestExecutePlanDoesNotBlockOnContinue(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"plan", "--plain"}, "", app); code != 0 {
		t.Fatalf("plan exit code = %d", code)
	}
	out, _, code := captureExecute(t, []string{"runs"}, "", app)
	if code != 0 {
		t.Fatalf("runs exit code = %d", code)
	}
	if !strings.Contains(out, "(plan)") {
		t.Fatalf("runs did not mark the plan run:\n%s", out)
	}
}

func TestExecuteContinueResumesASuccessfulRun(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"run", "--plain"}, "", app); code != 0 {
		t.Fatal("first run failed")
	}
	out, _, code := captureExecute(t, []string{"run", "--plain", "--continue", "last"}, "", app)
	if code != 0 {
		t.Fatalf("continued run exit code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "resumed") {
		t.Fatalf("continued run did not resume the earlier build:\n%s", out)
	}
}

func TestExecuteDotAfterARun(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"run", "--plain"}, "", app); code != 0 {
		t.Fatal("run failed")
	}
	out, _, code := captureExecute(t, []string{"dot"}, "", app)
	if code != 0 {
		t.Fatalf("dot exit code = %d", code)
	}
	if !strings.Contains(out, "digraph") || !strings.Contains(out, "build") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestExecuteFlamegraphAfterARun(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"run", "--plain"}, "", app); code != 0 {
		t.Fatal("run failed")
	}
	out, _, code := captureExecute(t, []string{"flamegraph"}, "", app)
	if code != 0 {
		t.Fatalf("flamegraph exit code = %d", code)
	}
	if !strings.Contains(out, "traceEvents") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestExecuteLogsAfterARun(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"run", "--plain"}, "", app); code != 0 {
		t.Fatal("run failed")
	}
	out, _, code := captureExecute(t, []string{"logs"}, "", app)
	if code != 0 {
		t.Fatalf("logs exit code = %d", code)
	}
	if !strings.Contains(out, "build") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestExecuteStateAfterARun(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"run", "--plain"}, "", app); code != 0 {
		t.Fatal("run failed")
	}
	out, _, code := captureExecute(t, []string{"state"}, "", app)
	if code != 0 {
		t.Fatalf("state exit code = %d", code)
	}
	if !strings.Contains(out, "testapp/build") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestExecuteNoStateMeansNoJournal(t *testing.T) {
	app := testApp(t, false)
	if _, _, code := captureExecute(t, []string{"run", "--plain", "--no-state"}, "", app); code != 0 {
		t.Fatal("run failed")
	}
	out, _, code := captureExecute(t, []string{"runs"}, "", app)
	if code != 0 {
		t.Fatalf("runs exit code = %d", code)
	}
	if !strings.Contains(out, "no runs recorded") {
		t.Fatalf("expected no runs recorded, got:\n%s", out)
	}
}

// TestExecuteInjectsThePrompter checks that a workflow can pull the run's
// prompter out of its context and drive an approval through it. This is the
// plumbing that lets a confirming world use the active display's prompt
// instead of one that writes over it.
func TestExecuteInjectsThePrompter(t *testing.T) {
	dir := t.TempDir()
	var performed bool
	app := cli.App{
		Name: "testapp",
		Flow: func(ctx *work.Context) (string, error) {
			p := world.PrompterFromContext(ctx)
			if p == nil {
				return "", errors.New("no prompter in the workflow context")
			}
			w := world.Confirm(world.Real(), p)
			return work.Do(ctx, "act", func(ctx *work.Context) (string, error) {
				return world.Perform(ctx, w, world.Effect[string]{
					What:    "touch the world",
					Do:      func() (string, error) { performed = true; return "done", nil },
					Instead: "skipped",
				})
			})
		},
		DefaultStatePath: filepath.Join(dir, "state.json"),
		DefaultLogDir:    filepath.Join(dir, "logs"),
	}
	out, _, code := captureExecute(t, []string{"run", "--plain"}, "y\n", app)
	if code != 0 {
		t.Fatalf("code = %d, want 0; output:\n%s", code, out)
	}
	if !performed {
		t.Fatalf("the approved effect never ran; output:\n%s", out)
	}
}

func TestUserStatePathIsUnderXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg")
	got := cli.UserStatePath("widget")
	want := filepath.Join("/xdg", "cascade", "widget.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
