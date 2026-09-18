package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdbrown/cascade/history"
	"github.com/mdbrown/cascade/world"
)

func TestViewRendersRowsAndPressToAbortHint(t *testing.T) {
	m := newModel(func() {})
	m.width, m.height = 80, 24
	m.apply(history.Event{Kind: history.RunStarted, Time: time.Now()})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release", Status: history.Running, Time: time.Now()})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release/build", Parent: "release", Status: history.Running, Time: time.Now()})

	out := m.View()
	if !strings.Contains(out, "release") || !strings.Contains(out, "build") {
		t.Fatalf("View missing expected rows:\n%s", out)
	}
	if !strings.Contains(out, "press q to abort") {
		t.Fatalf("View missing the abort hint:\n%s", out)
	}
}

func TestViewIsEmptyBeforeAnyRows(t *testing.T) {
	m := newModel(func() {})
	if m.View() != "" {
		t.Fatalf("View before any rows = %q, want empty", m.View())
	}
}

func TestViewRendersAnApprovalPrompt(t *testing.T) {
	m := newModel(func() {})
	m.width, m.height = 80, 24
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release/publish", Status: history.Running})
	m.approvals = []pendingApproval{{req: world.Request{
		Task: "release/publish", Target: "upload dist/",
	}, respChan: make(chan world.Decision, 1)}}

	out := m.View()
	for _, want := range []string{"APPROVE", "release/publish", "Effect:", "upload dist/", "[y]es"} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing %q:\n%s", want, out)
		}
	}
}

func TestUpdateQKeyCancelsTheRun(t *testing.T) {
	canceled := false
	m := newModel(func() { canceled = true })
	m.apply(history.Event{Kind: history.TaskStarted, Path: "a", Status: history.Running})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	nm := next.(*model)
	if !canceled || !nm.aborting {
		t.Fatalf("q did not cancel: canceled=%v aborting=%v", canceled, nm.aborting)
	}
}

func TestUpdateApprovalKeysRespondAndAdvance(t *testing.T) {
	m := newModel(func() {})
	resp := make(chan world.Decision, 1)
	m.approvals = []pendingApproval{{req: world.Request{Task: "a"}, respChan: resp}}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	nm := next.(*model)
	if len(nm.approvals) != 0 {
		t.Fatalf("approval queue not drained: %+v", nm.approvals)
	}
	select {
	case dec := <-resp:
		if dec != world.ApproveOnce {
			t.Fatalf("got %v, want ApproveOnce", dec)
		}
	default:
		t.Fatal("no decision sent")
	}
}

func TestUpdateApproveAllRespondsToEveryQueuedApproval(t *testing.T) {
	m := newModel(func() {})
	r1, r2 := make(chan world.Decision, 1), make(chan world.Decision, 1)
	m.approvals = []pendingApproval{
		{req: world.Request{Task: "a"}, respChan: r1},
		{req: world.Request{Task: "b"}, respChan: r2},
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	nm := next.(*model)
	if !nm.approvedAll || len(nm.approvals) != 0 {
		t.Fatalf("got approvedAll=%v approvals=%+v", nm.approvedAll, nm.approvals)
	}
	for _, ch := range []chan world.Decision{r1, r2} {
		select {
		case dec := <-ch:
			if dec != world.ApproveAll {
				t.Fatalf("got %v, want ApproveAll", dec)
			}
		default:
			t.Fatal("no decision sent")
		}
	}
}

func TestUpdateWindowSizeMsgResizes(t *testing.T) {
	m := newModel(func() {})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	nm := next.(*model)
	if nm.width != 40 || nm.height != 10 {
		t.Fatalf("got %dx%d", nm.width, nm.height)
	}
}

func TestUpdateTickAdvancesElapsedForRunningRows(t *testing.T) {
	m := newModel(func() {})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "a", Status: history.Running, Time: time.Now().Add(-time.Second)})
	next, _ := m.Update(tickMsg(time.Now()))
	nm := next.(*model)
	if nm.rows[0].elapsed <= 0 {
		t.Fatalf("elapsed = %v, want > 0", nm.rows[0].elapsed)
	}
}

func TestTruncateVisibleIgnoresANSIEscapes(t *testing.T) {
	styled := "\x1b[31mred text that is somewhat long\x1b[0m"
	out := truncateVisible(styled, 10)
	if !strings.Contains(out, "…") {
		t.Fatalf("got %q, want it truncated", out)
	}
}

func TestIsTerminalIsFalseForNil(t *testing.T) {
	if IsTerminal(nil) {
		t.Fatal("IsTerminal(nil) = true")
	}
}
