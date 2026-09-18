package history

import (
	"testing"
	"time"
)

func TestJournalRecentPathsAndMostRecent(t *testing.T) {
	j := &Journal{}
	j.save(Run{ID: "r1", Tasks: map[string]Record{"a": {Status: Succeeded, Summary: "first"}}})
	j.save(Run{ID: "r2", Tasks: map[string]Record{"a": {Status: Failed, Summary: "second"}, "b": {Status: Succeeded}}})

	recent := j.Recent(1)
	if len(recent) != 1 || recent[0].ID != "r2" {
		t.Fatalf("Recent(1) = %+v", recent)
	}

	paths := j.Paths()
	if len(paths) != 2 || paths[0] != "a" || paths[1] != "b" {
		t.Fatalf("Paths() = %v", paths)
	}

	rec, runID, ok := j.MostRecent("a")
	if !ok || runID != "r2" || rec.Summary != "second" {
		t.Fatalf("MostRecent(a) = %+v, %q, %v", rec, runID, ok)
	}

	if _, _, ok := j.MostRecent("missing"); ok {
		t.Fatal("MostRecent found a path that was never recorded")
	}
}

func TestNilJournalMethodsAreSafe(t *testing.T) {
	var j *Journal
	if j.Recent(5) != nil {
		t.Fatal("nil Journal.Recent should return nil")
	}
	if j.Paths() != nil {
		t.Fatal("nil Journal.Paths should return nil")
	}
	if _, ok := j.Run("x"); ok {
		t.Fatal("nil Journal.Run should report false")
	}
	if _, ok := j.LastSuccessful("x", "fp"); ok {
		t.Fatal("nil Journal.LastSuccessful should report false")
	}
	if _, _, ok := j.MostRecent("x"); ok {
		t.Fatal("nil Journal.MostRecent should report false")
	}
}

func TestStringDurationMarshalUnmarshal(t *testing.T) {
	d := Duration(90 * time.Second)
	b, err := d.MarshalText()
	if err != nil || string(b) != "1m30s" {
		t.Fatalf("got %q, %v", b, err)
	}
	var got Duration
	if err := got.UnmarshalText(b); err != nil || time.Duration(got) != 90*time.Second {
		t.Fatalf("got %v, %v", got, err)
	}
	if err := got.UnmarshalText([]byte("not-a-duration")); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
}
