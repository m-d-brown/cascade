package interpreter

import (
	"path/filepath"
	"strings"
	"testing"
)

// warnOf parses yaml (which must be structurally valid) and returns the one
// warning that mentions want, failing if there isn't exactly a match.
func warnOf(t *testing.T, yaml, want string) string {
	t.Helper()
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var hits []string
	for _, w := range p.Warnings() {
		if strings.Contains(w, want) {
			hits = append(hits, w)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one warning mentioning %q, got %d:\n%s", want, len(hits), strings.Join(p.Warnings(), "\n"))
	}
	return hits[0]
}

func TestLintWarnings(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{
			"does nothing",
			"actions:\n  x: {description: idle}\n",
			`"x" does nothing`,
		},
		{
			"barrier with ignored fields",
			"actions:\n  a: {run: 'true'}\n  b: {needs: [a], produces: [out], every: 5m}\n",
			"has no run command, so `every` and `produces` are ignored",
		},
		{
			"sources without produces",
			"actions:\n  a: {run: make, sources: ['*.c']}\n",
			`"a" lists sources but no produces`,
		},
		{
			"allow-exit zero",
			"actions:\n  a: {run: x, allow-exit: [0]}\n",
			"allow-exit lists 0",
		},
		{
			"allow-exit out of range",
			"actions:\n  a: {run: x, allow-exit: [999]}\n",
			"allow-exit 999 is outside",
		},
		{
			"duplicate need",
			"actions:\n  dep: {run: 'true'}\n  a: {run: x, needs: [dep, dep]}\n",
			`"a" needs "dep" more than once`,
		},
		{
			"duplicate produces across actions",
			"actions:\n  a: {run: x, produces: [shared]}\n  b: {run: y, produces: [shared]}\n",
			`both declare they produce "shared"`,
		},
		{
			"duplicate produces within one action",
			"actions:\n  a: {run: x, produces: [out, out]}\n",
			`lists "out" in produces more than once`,
		},
		{
			"every not positive",
			"actions:\n  a: {run: x, every: -5m}\n",
			`every "-5m" is not positive`,
		},
		{
			"glob in produces",
			"actions:\n  a: {run: x, produces: ['dist/*.tar']}\n",
			`produces "dist/*.tar" looks like a glob`,
		},
		{
			"unusable env name",
			"actions:\n  a:\n    run: x\n    env: {'A=B': c}\n",
			`env has a name "A=B"`,
		},
		{
			"sources walks the whole filesystem",
			"actions:\n  a: {run: x, produces: [o], sources: ['/**']}\n",
			"walks the entire filesystem",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { warnOf(t, tc.yaml, tc.want) })
	}
}

func TestLintCleanFileHasNoWarnings(t *testing.T) {
	p, err := Parse([]byte(`
actions:
  fetch: {run: "git pull"}
  build:
    run: go build -o out ./...
    needs: [fetch]
    produces: [out]
    sources: ["**/*.go"]
  done: {needs: [build]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if w := p.Warnings(); len(w) != 0 {
		t.Fatalf("clean cascade warned:\n%s", strings.Join(w, "\n"))
	}
}

func TestLintMissingDir(t *testing.T) {
	warnOf(t, "actions:\n  a: {run: x, dir: /no/such/dir/anywhere}\n",
		`dir "/no/such/dir/anywhere" does not exist`)
}

func TestLintDirExistsIsNotWarned(t *testing.T) {
	dir := t.TempDir()
	p, err := Parse([]byte("actions:\n  a: {run: x, dir: " + dir + "}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.Warnings() {
		if strings.Contains(w, "does not exist") {
			t.Fatalf("warned about a dir that exists: %s", w)
		}
	}
}

func TestLintDirProducedByAnotherActionIsNotWarned(t *testing.T) {
	made := filepath.Join(t.TempDir(), "workdir")
	yaml := "actions:\n" +
		"  setup: {run: 'mkdir -p " + made + "', produces: [" + made + "]}\n" +
		"  build: {run: 'make', needs: [setup], dir: " + made + "}\n"
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.Warnings() {
		if strings.Contains(w, "does not exist") {
			t.Fatalf("warned about a dir another action produces: %s", w)
		}
	}
}
