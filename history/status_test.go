package history

import "testing"

func TestStatusTerminalAndOK(t *testing.T) {
	cases := []struct {
		s            Status
		terminal, ok bool
	}{
		{Pending, false, false},
		{Running, false, false},
		{Succeeded, true, true},
		{Skipped, true, true},
		{Resumed, true, true},
		{Failed, true, false},
		{Canceled, true, false},
	}
	for _, c := range cases {
		if got := c.s.Terminal(); got != c.terminal {
			t.Errorf("%v.Terminal() = %v, want %v", c.s, got, c.terminal)
		}
		if got := c.s.OK(); got != c.ok {
			t.Errorf("%v.OK() = %v, want %v", c.s, got, c.ok)
		}
	}
}

func TestStatusSymbolIsNonEmptyForEveryKnownStatus(t *testing.T) {
	for _, s := range []Status{Pending, Running, Succeeded, Skipped, Resumed, Failed, Canceled} {
		if s.Symbol() == "" {
			t.Errorf("%v has no symbol", s)
		}
	}
}

func TestStatusMarshalUnmarshalTextRoundTrip(t *testing.T) {
	for _, s := range []Status{Succeeded, Failed, Resumed} {
		b, err := s.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var got Status
		if err := got.UnmarshalText(b); err != nil {
			t.Fatal(err)
		}
		if got != s {
			t.Errorf("round trip: got %v, want %v", got, s)
		}
	}
	var s Status
	if err := s.UnmarshalText([]byte("not-a-status")); err == nil {
		t.Fatal("expected an error for an unknown status")
	}
}

func TestUnknownStatusStringsAsUnknown(t *testing.T) {
	if Status(200).String() != "unknown" {
		t.Fatal("an unrecognized status should string as unknown")
	}
}
