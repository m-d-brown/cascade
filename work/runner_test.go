package work

import (
	"errors"
	"testing"
)

func TestResultErrJoinsFailures(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, _ = Do(ctx, "a", func(ctx *Context) (bool, error) { return false, errors.New("a broke") })
		return errors.New("a broke")
	}, Options{})
	err := res.Err()
	if err == nil {
		t.Fatal("Err() = nil, want an error")
	}
}

func TestResultErrIsNilForASuccessfulRun(t *testing.T) {
	res := runFor(t, func(ctx *Context) error { return nil }, Options{})
	if err := res.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
}

func TestResultSortedResultsListsCalleesBeforeTheirCaller(t *testing.T) {
	res := runFor(t, func(ctx *Context) error {
		_, _ = Do(ctx, "first", func(ctx *Context) (bool, error) { return true, nil })
		_, _ = Do(ctx, "second", func(ctx *Context) (bool, error) { return true, nil })
		return nil
	}, Options{})
	sorted := res.SortedResults()
	var names []string
	for _, r := range sorted {
		names = append(names, r.Name)
	}
	// first and second finish before the root call that made them, and
	// among themselves in the order they finished.
	if len(names) != 3 || names[0] != "first" || names[1] != "second" || names[2] != "run" {
		t.Fatalf("got %v", names)
	}
}

func TestDecodeAsRoundTripsThroughJSONWhenTheLiveTypeDoesNotAssert(t *testing.T) {
	// A value shaped like what the journal hands back — a plain map, the
	// way an int comes back a float64 — must still decode into T.
	raw := map[string]any{"a": float64(1)}
	type shape struct {
		A int `json:"a"`
	}
	got := decodeAs[shape](raw)
	if got.A != 1 {
		t.Fatalf("got %+v", got)
	}
	if decodeAs[string](nil) != "" {
		t.Fatal("decodeAs(nil) should be the zero value")
	}
}

func TestDecodeAsPanicsOnAnIncompatibleShape(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for an incompatible recorded value")
		}
	}()
	decodeAs[int](map[string]any{"not": "a number"})
}

func TestJournalSafeAcceptsPlainJSONShapesAndRejectsStructs(t *testing.T) {
	cases := []struct {
		v    any
		want bool
	}{
		{nil, true},
		{"s", true},
		{42, true},
		{3.14, true},
		{true, true},
		{[]any{"a", 1}, true},
		{map[string]any{"a": 1}, true},
		{struct{ X int }{1}, false},
		{[]any{struct{}{}}, false},
	}
	for _, c := range cases {
		if got := journalSafe(c.v); got != c.want {
			t.Errorf("journalSafe(%#v) = %v, want %v", c.v, got, c.want)
		}
	}
}
