package history

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreRoundTripsARun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewFileStore(path)

	run := Run{
		ID: "r1", Status: Succeeded, Input: "v1.0",
		Tasks: map[string]Record{
			"run/build": {Path: "run/build", Status: Succeeded, Summary: "built", Value: "dist/x", HasValue: true},
		},
		Started: time.Now(),
	}
	if err := s.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	reloaded := NewFileStore(path)
	j, err := reloaded.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := j.Run("r1")
	if !ok {
		t.Fatal("run r1 not found after reload")
	}
	rec := got.Tasks["run/build"]
	if rec.Summary != "built" || rec.Value != "dist/x" {
		t.Fatalf("got %+v", rec)
	}
}

func TestFileStoreLoadOfMissingFileIsEmpty(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	j, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Runs) != 0 {
		t.Fatalf("got %d runs, want 0", len(j.Runs))
	}
}

func TestJournalKeepsBoundedHistory(t *testing.T) {
	j := &Journal{}
	for i := 0; i < keepRuns+5; i++ {
		j.save(Run{ID: string(rune('a' + i))})
	}
	if len(j.Runs) != keepRuns {
		t.Fatalf("got %d runs, want %d", len(j.Runs), keepRuns)
	}
}

func TestRunRestorableRejectsPartialAndSecretAndMismatchedFingerprint(t *testing.T) {
	run := Run{Tasks: map[string]Record{
		"ok":      {Status: Succeeded, Fingerprint: "fp"},
		"partial": {Status: Succeeded, Fingerprint: "fp", Partial: true},
		"secret":  {Status: Succeeded, Fingerprint: "fp", Secret: true},
		"failed":  {Status: Failed, Fingerprint: "fp"},
		"resumed": {Status: Resumed, Fingerprint: "fp"},
		"skipped": {Status: Skipped, Fingerprint: "fp"},
	}}
	cases := []struct {
		path        string
		fingerprint string
		want        bool
	}{
		{"ok", "fp", true},
		{"ok", "other", false},
		{"partial", "fp", false},
		{"secret", "fp", false},
		{"failed", "fp", false},
		{"resumed", "fp", true},
		{"skipped", "fp", false}, // Skipped is OK() for the run's exit code, but was never "work that happened"
		{"missing", "fp", false},
	}
	for _, c := range cases {
		_, ok := run.Restorable(c.path, c.fingerprint)
		if ok != c.want {
			t.Errorf("Restorable(%q, %q) = %v, want %v", c.path, c.fingerprint, ok, c.want)
		}
	}
}
