package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdbrown/cascade/history"
)

// flowLine renders one flow.log line: timestamp, level, call path, message.
func flowLine(path, msg string) string {
	return fmt.Sprintf("12:00:00.000 INFO  %-24s %s\n", path, msg)
}

// writeFlow writes a flow.log built from (callPath, message) pairs.
func writeFlow(t *testing.T, file string, entries ...[2]string) {
	t.Helper()
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(flowLine(e[0], e[1]))
	}
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// flowLog writes a fresh flow.log and returns its path.
func flowLog(t *testing.T, entries ...[2]string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "flow.log")
	writeFlow(t, f, entries...)
	return f
}

// browseModel is a finished, keep-up run of three calls, ready to step through.
func browseModel(t *testing.T) *model {
	t.Helper()
	m := newModel(func() {})
	m.keepUp = true
	m.width, m.height = 80, 24
	now := time.Now()
	lf := flowLog(t,
		[2]string{"release/build", "compiling"},
		[2]string{"release/build", "linking"},
		[2]string{"release/build", "built ok"},
		[2]string{"release/publish", "uploading"},
		[2]string{"release/publish", "uploaded"},
	)
	m.apply(history.Event{Kind: history.RunStarted, Time: now, LogPath: lf})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release", Status: history.Running, Time: now})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release/build", Parent: "release", Status: history.Running, Time: now})
	m.apply(history.Event{Kind: history.TaskStarted, Path: "release/publish", Parent: "release", Status: history.Running, Time: now})
	for _, p := range []string{"release/build", "release/publish", "release"} {
		m.apply(history.Event{Kind: history.TaskFinished, Path: p, Status: history.Succeeded,
			Result: &history.TaskResult{Status: history.Succeeded, Summary: "ok", Duration: time.Second}})
	}
	m.apply(history.Event{Kind: history.RunFinished, Outcome: &history.Result{}})
	return m
}

func send(m *model, msg tea.Msg) *model {
	next, _ := m.Update(msg)
	return next.(*model)
}

func TestRunFinishedWithKeepUpBrowsesInsteadOfQuitting(t *testing.T) {
	m := newModel(func() {})
	m.keepUp = true
	m.apply(history.Event{Kind: history.TaskStarted, Path: "a", Status: history.Running})
	cmd := m.apply(history.Event{Kind: history.RunFinished, Outcome: &history.Result{}})
	if cmd != nil {
		t.Fatal("a keep-up run should not quit on RunFinished")
	}
	if !m.browsing || m.cursor != "a" {
		t.Fatalf("browsing=%v cursor=%q, want browsing on the first row", m.browsing, m.cursor)
	}
}

func TestBrowseCursorStepsThroughRowsAndClampsAtTheEnds(t *testing.T) {
	m := browseModel(t)
	if got := m.cursor; got != "release" {
		t.Fatalf("cursor = %q, want the first row", got)
	}
	send(m, tea.KeyMsg{Type: tea.KeyDown})
	send(m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.cursor; got != "release/publish" {
		t.Fatalf("cursor = %q, want release/publish after two downs", got)
	}
	send(m, tea.KeyMsg{Type: tea.KeyDown}) // past the end
	if got := m.cursor; got != "release/publish" {
		t.Fatalf("cursor = %q, want it clamped at the last row", got)
	}
	for i := 0; i < 5; i++ {
		send(m, tea.KeyMsg{Type: tea.KeyUp})
	}
	if got := m.cursor; got != "release" {
		t.Fatalf("cursor = %q, want it clamped at the first row", got)
	}
}

func TestEnterOpensTheSelectedStepsLog(t *testing.T) {
	m := browseModel(t)
	send(m, tea.KeyMsg{Type: tea.KeyDown}) // release/build
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.logView == nil || m.logView.path != "release/build" {
		t.Fatalf("logView = %+v, want release/build's log", m.logView)
	}
	// Only release/build's lines, not release/publish's.
	if len(m.logView.lines) != 3 {
		t.Fatalf("logView lines = %v, want the 3 tagged release/build", m.logView.lines)
	}
	out := m.View()
	for _, want := range []string{"build", "compiling", "linking", "built ok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "uploading") {
		t.Fatalf("log view leaked another call's lines:\n%s", out)
	}
}

func TestEnterAtTheEndOfALogMovesToTheNextStep(t *testing.T) {
	m := browseModel(t)
	send(m, tea.KeyMsg{Type: tea.KeyDown})  // release/build
	send(m, tea.KeyMsg{Type: tea.KeyEnter}) // open its log — short, so already at the end
	if off := m.logView.offset; off != m.maxLogOffset() {
		t.Fatalf("offset = %d, want the whole short log already in view (%d)", off, m.maxLogOffset())
	}
	send(m, tea.KeyMsg{Type: tea.KeyEnter}) // at the end: roll on to the next step
	if m.logView == nil || m.logView.path != "release/publish" {
		t.Fatalf("logView = %+v, want it advanced to release/publish", m.logView)
	}
	if m.cursor != "release/publish" {
		t.Fatalf("cursor = %q, want it to follow the log to release/publish", m.cursor)
	}
	if !strings.Contains(m.View(), "uploaded") {
		t.Fatalf("expected the next step's log on screen:\n%s", m.View())
	}
}

func TestEnterPagesDownALongLogBeforeAdvancing(t *testing.T) {
	m := browseModel(t)
	m.height = 8 // body of ~4 lines
	entries := make([][2]string, 0, 30)
	for i := 0; i < 30; i++ {
		entries = append(entries, [2]string{"release/build", "line"})
	}
	writeFlow(t, m.logFile, entries...)

	m.cursor = "release/build"
	send(m, tea.KeyMsg{Type: tea.KeyEnter}) // open at the top
	if m.logView.offset != 0 {
		t.Fatalf("offset = %d, want it opened at the top", m.logView.offset)
	}
	send(m, tea.KeyMsg{Type: tea.KeyEnter}) // still a long way from the end
	if m.logView.path != "release/build" || m.logView.offset == 0 {
		t.Fatalf("enter should have paged down, not advanced: %+v", m.logView)
	}
}

func TestEscLeavesTheLogPaneThenLeavesBrowsing(t *testing.T) {
	m := browseModel(t)
	m.finished = false // pretend the run is still going
	send(m, tea.KeyMsg{Type: tea.KeyDown})
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.logView == nil {
		t.Fatal("log pane did not open")
	}
	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.logView != nil || !m.browsing {
		t.Fatalf("esc should close the log pane and stay in browse: logView=%v browsing=%v", m.logView, m.browsing)
	}
	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.browsing {
		t.Fatal("a second esc should drop back to watching the run")
	}
}

func TestOpenLogFollowsAGrowingFileOnTick(t *testing.T) {
	m := browseModel(t)
	m.finished = false // the run is still going
	m.height = 6
	writeFlow(t, m.logFile,
		[2]string{"release/build", "a"},
		[2]string{"release/build", "b"},
	)
	m.cursor = "release/build"
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	send(m, tea.KeyMsg{Type: tea.KeyEnd}) // pin to the end

	entries := make([][2]string, 0, 7)
	for _, s := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		entries = append(entries, [2]string{"release/build", s})
	}
	writeFlow(t, m.logFile, entries...)
	send(m, tickMsg(time.Now()))

	if got := len(m.logView.lines); got != 7 {
		t.Fatalf("tick did not reload the growing log: %d lines", got)
	}
	if m.logView.offset != m.maxLogOffset() {
		t.Fatalf("a log followed to the end should stay pinned: offset=%d max=%d", m.logView.offset, m.maxLogOffset())
	}
}

func TestBrowseViewDrawsEveryRowWithACursorMarker(t *testing.T) {
	m := browseModel(t)
	out := m.View()
	for _, want := range []string{"release", "build", "publish", "▸"} {
		if !strings.Contains(out, want) {
			t.Fatalf("browse view missing %q:\n%s", want, out)
		}
	}
}

func TestRunStartedCarriesTheRunLogFile(t *testing.T) {
	m := newModel(nil)
	m.apply(history.Event{Kind: history.RunStarted, LogPath: "/logs/run/flow.log"})
	if m.logFile != "/logs/run/flow.log" {
		t.Fatalf("logFile = %q, want it set from RunStarted", m.logFile)
	}
}

func TestCombinedLinePathReadsTheThirdField(t *testing.T) {
	got := combinedLinePath("12:00:00.000 INFO  release/build            compiling ./...  now")
	if got != "release/build" {
		t.Fatalf("combinedLinePath = %q, want release/build", got)
	}
	if combinedLinePath("garbage") != "" {
		t.Fatalf("combinedLinePath on a short line should be empty")
	}
}
