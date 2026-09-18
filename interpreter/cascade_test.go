package interpreter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseUsesTheKeyAsTheActionName(t *testing.T) {
	r, err := Parse([]byte(`
name: demo
actions:
  fetch:
    run: echo fetch
  build:
    run: echo build
    needs: [fetch]
`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "demo" {
		t.Errorf("Name() = %q, want demo", r.Name())
	}
	if got := r.Order(); !reflect.DeepEqual(got, []string{"fetch", "build"}) {
		t.Errorf("Order() = %v, want [fetch build]", got)
	}
	if _, ok := r.Actions()["build"]; !ok {
		t.Errorf("Actions() missing build: %v", r.Actions())
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want string
	}{
		"unknown action field":           {"actions:\n  a:\n    runn: echo hi\n", `line 3: unknown field "runn" for action "a"`},
		"unknown field lists valid ones": {"actions:\n  a:\n    runn: echo hi\n", "valid: run, needs, produces"},
		"unknown top-level field":        {"nope: 1\nactions:\n  a: {run: x}\n", `line 1: unknown field "nope" at the top level`},
		"run given a list":               {"actions:\n  a: {run: [echo, hi]}\n", "must be a single shell command"},
		"actions given a list":           {"actions:\n  - {run: x}\n", "must be a map of name to action"},
		"action defined twice":           {"actions:\n  a: {run: x}\n  a: {run: y}\n", `"a" is defined twice`},
		"slash in name":                  {"actions:\n  \"a/b\": {run: x}\n", "cannot contain '/'"},
		"unknown need":                   {"actions:\n  a:\n    needs: [ghost]\n", "ghost"},
		"self need":                      {"actions:\n  a:\n    needs: [a]\n", "needs itself"},
		"cycle":                          {"actions:\n  a: {needs: [b]}\n  b: {needs: [c]}\n  c: {needs: [a]}\n", "cycle"},
		"no actions":                     {"name: empty\n", "no actions"},
		"empty document":                 {"\n", "empty"},
		"bad every":                      {"actions:\n  a:\n    run: x\n    every: soon\n", "every"},
		"bad progress-every":             {"actions:\n  a:\n    run: x\n    progress-every: yesterday\n", "progress-every"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("want an error, got nil")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseCoercesBareScalarIntoList(t *testing.T) {
	p, err := Parse([]byte(`
actions:
  fetch: {run: "echo f"}
  build:
    run: "echo b"
    needs: fetch
    sources: "*.go"
    produces: out
`))
	if err != nil {
		t.Fatal(err)
	}
	b := p.Actions()["build"]
	if !reflect.DeepEqual(b.Needs, []string{"fetch"}) ||
		!reflect.DeepEqual(b.Sources, []string{"*.go"}) ||
		!reflect.DeepEqual(b.Produces, []string{"out"}) {
		t.Fatalf("scalars not coerced to one-element lists: %+v", b)
	}
}

func TestLoadFriendlyErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil ||
		!strings.Contains(err.Error(), "no cascade file at") {
		t.Fatalf("missing file: got %v", err)
	}
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory: got %v", err)
	}
}

func TestScheduleWaves(t *testing.T) {
	// a diamond: a → {b, c} → d
	r, err := Parse([]byte(`
actions:
  a: {run: 'true'}
  b: {run: 'true', needs: [a]}
  c: {run: 'true', needs: [a]}
  d: {run: 'true', needs: [b, c]}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a"}, {"b", "c"}, {"d"}}
	if !reflect.DeepEqual(r.waves, want) {
		t.Fatalf("waves = %v, want %v", r.waves, want)
	}
}

func TestFindCycleNamesTheLoop(t *testing.T) {
	actions := map[string]Action{
		"a": {Needs: []string{"b"}},
		"b": {Needs: []string{"a"}},
		"x": {},
	}
	cycle := findCycle(actions, []string{"a", "b", "x"})
	if len(cycle) < 3 || cycle[0] != cycle[len(cycle)-1] {
		t.Fatalf("findCycle = %v, want a closed loop", cycle)
	}
}

func TestLoadDefaultsNameToFileBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.yaml")
	if err := os.WriteFile(path, []byte("actions:\n  a: {run: 'true'}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "backup" {
		t.Fatalf("Name() = %q, want backup", r.Name())
	}
}
