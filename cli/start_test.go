package cli_test

import (
	"strings"
	"testing"
)

func TestStartQuitsImmediately(t *testing.T) {
	out, _, code := captureExecute(t, []string{"start"}, "q\n", testApp(t, false))
	if code != 0 {
		t.Fatalf("code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "no previous runs") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestStartEOFExitsCleanly(t *testing.T) {
	out, _, code := captureExecute(t, []string{"start"}, "", testApp(t, false))
	if code != 0 {
		t.Fatalf("code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "[enter] start a new run") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestStartEnterRunsTheWorkflow(t *testing.T) {
	out, _, code := captureExecute(t, []string{"start"}, "\n", testApp(t, false))
	if code != 0 {
		t.Fatalf("code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "testapp/build") {
		t.Fatalf("enter did not run the workflow:\n%s", out)
	}
}

func TestStartInvalidChoiceReprompts(t *testing.T) {
	out, _, code := captureExecute(t, []string{"start"}, "bogus\nq\n", testApp(t, false))
	if code != 0 {
		t.Fatalf("code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, `"bogus" is not one of the choices`) {
		t.Fatalf("got:\n%s", out)
	}
}

func TestStartPlanThenQuit(t *testing.T) {
	out, errOut, code := captureExecute(t, []string{"start"}, "p\nq\n", testApp(t, false))
	if code != 0 {
		t.Fatalf("code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(errOut, "marking this run as a plan") {
		t.Fatalf("[p] did not run a plan; stderr:\n%s", errOut)
	}
}

func TestStartOffersToFinishAnInterruptedRun(t *testing.T) {
	app := testApp(t, true) // fails, leaving a failed (continuable) run behind
	if _, _, code := captureExecute(t, []string{"run", "--plain"}, "", app); code == 0 {
		t.Fatal("expected the setup run to fail")
	}

	app2 := app
	app2.Flow = testApp(t, false).Flow // now let it succeed if continued
	out, errOut, code := captureExecute(t, []string{"start"}, "1\n", app2)
	if code != 0 {
		t.Fatalf("code = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "[1] finish run 1") {
		t.Fatalf("start did not offer to finish the failed run:\n%s", out)
	}
	if !strings.Contains(errOut, "continuing run") {
		t.Fatalf("choosing 1 did not continue it; stderr:\n%s", errOut)
	}
}
