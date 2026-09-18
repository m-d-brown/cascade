package history

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLogDirCreatesAUniqueDirectoryAndFiles(t *testing.T) {
	base := t.TempDir()
	d, err := NewLogDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	if d.Run() == "" {
		t.Fatal("Run() is empty")
	}
	if _, err := os.Stat(filepath.Join(d.Dir, "flow.log")); err != nil {
		t.Fatalf("flow.log not created: %v", err)
	}
	if d.Combined() != filepath.Join(d.Dir, "flow.log") {
		t.Fatalf("Combined() = %q, want the flow.log path", d.Combined())
	}

	d.WriteCombined("release/build", slog.LevelInfo, "a combined line", time.Now())
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	combined, err := os.ReadFile(filepath.Join(d.Dir, "flow.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(combined), "a combined line") {
		t.Fatalf("combined log missing the line: %q", combined)
	}
	if !strings.Contains(string(combined), "release/build") {
		t.Fatalf("combined line not tagged with its call path: %q", combined)
	}

	// One text log, no file per call.
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "flow.log" && e.Name() != "run.jsonl" {
			t.Fatalf("unexpected file in the log dir: %s", e.Name())
		}
	}
}

func TestNewLogDirDisambiguatesTwoRunsInTheSameSecond(t *testing.T) {
	base := t.TempDir()
	d1, err := NewLogDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d1.Close() }()
	d2, err := NewLogDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()
	if d1.Dir == d2.Dir {
		t.Fatalf("two runs got the same log directory: %s", d1.Dir)
	}
}

func TestLogDirEventsWriterIsCreatedOnFirstUse(t *testing.T) {
	base := t.TempDir()
	d, err := NewLogDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	w := d.Events()
	if _, err := w.Write([]byte(`{"hello":"world"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(d.Dir, "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("got %q", data)
	}
}

func TestNilLogDirMethodsAreSafeNoOps(t *testing.T) {
	var d *LogDir
	if d.Run() != "" {
		t.Fatal("nil LogDir.Run should be empty")
	}
	if d.Events() == nil {
		t.Fatal("nil LogDir.Events should return io.Discard, not nil")
	}
	if d.Combined() != "" {
		t.Fatalf("nil LogDir.Combined should be empty, got %q", d.Combined())
	}
	d.WriteCombined("x", slog.LevelInfo, "msg", time.Now()) // must not panic
	if err := d.Close(); err != nil {
		t.Fatalf("Close on nil LogDir: %v", err)
	}
}
