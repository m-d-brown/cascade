package ui

import (
	"strings"
	"testing"

	"github.com/m-d-brown/cascade/history"
)

func TestFlattenAllFoldsNothing(t *testing.T) {
	rows := []row{
		{path: "r", name: "r", status: history.Succeeded},
		{path: "r/a", name: "a", parent: "r", status: history.Succeeded},
		{path: "r/b", name: "b", parent: "r", status: history.Succeeded},
	}
	roots := buildForest(rows)

	// flattenTree collapses a subtree with nothing running in it.
	if got := len(flattenTree(roots, 0)); got == 3 {
		t.Fatalf("flattenTree drew all %d rows; expected it to fold the idle subtree", got)
	}

	items := flattenAll(roots)
	if len(items) != 3 {
		t.Fatalf("flattenAll drew %d rows, want 3", len(items))
	}
	for _, it := range items {
		if it.isSummary {
			t.Fatalf("flattenAll produced a summary line: %+v", it)
		}
	}
}

func TestBuildForestNestsByParentPath(t *testing.T) {
	rows := []row{
		{path: "release", name: "release", parent: ""},
		{path: "release/build", name: "build", parent: "release"},
		{path: "release/build/generate", name: "generate", parent: "release/build"},
		{path: "release/publish", name: "publish", parent: "release"},
	}
	roots := buildForest(rows)
	if len(roots) != 1 || roots[0].row.path != "release" {
		t.Fatalf("roots = %+v", roots)
	}
	root := roots[0]
	if len(root.children) != 2 {
		t.Fatalf("release has %d children, want 2", len(root.children))
	}
	var build, publish *treeNode
	for _, c := range root.children {
		switch c.row.path {
		case "release/build":
			build = c
		case "release/publish":
			publish = c
		}
	}
	if build == nil || publish == nil {
		t.Fatalf("missing expected children: %+v", root.children)
	}
	if len(build.children) != 1 || build.children[0].row.path != "release/build/generate" {
		t.Fatalf("build's children: %+v", build.children)
	}
}

// drawn renders items as plain text, with "*" for each status glyph.
func drawn(items []renderedItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.node == nil {
			b.WriteString(it.prefix + "\n")
			continue
		}
		b.WriteString(it.prefix + "*" + it.right + " " + it.node.row.name + "\n")
	}
	return b.String()
}

func TestDrawForestDrawsDependentSiblingsAsLanes(t *testing.T) {
	rows := []row{
		{path: "build", name: "build"},
		{path: "build/deps", name: "deps", parent: "build"},
		{path: "build/compile", name: "compile", parent: "build", after: []string{"build/deps"}},
		{path: "build/lint", name: "lint", parent: "build", after: []string{"build/deps"}},
		{path: "build/test", name: "test", parent: "build", after: []string{"build/compile"}},
		{path: "build/package", name: "package", parent: "build", after: []string{"build/test", "build/lint"}},
		{path: "build/release", name: "release", parent: "build", after: []string{"build/package"}},
	}
	want := `* build
  * deps
  ├─╮
  * │ compile
  │ * lint
  * │ test
  ├─╯
  * package
  * release
`
	if got := drawn(flattenAll(buildForest(rows))); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDrawForestKeepsTreeConnectorsWithoutAfter(t *testing.T) {
	rows := []row{
		{path: "r", name: "r"},
		{path: "r/a", name: "a", parent: "r"},
		{path: "r/a/x", name: "x", parent: "r/a"},
		{path: "r/b", name: "b", parent: "r"},
	}
	want := `* r
  ├─ * a
  │  └─ * x
  └─ * b
`
	if got := drawn(flattenAll(buildForest(rows))); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFlattenTreeFoldsAFinishedSubtreeWhenSpaceIsTight(t *testing.T) {
	rows := []row{
		{path: "release", name: "release", parent: "", status: history.Running},
		{path: "release/a", name: "a", parent: "release", status: history.Succeeded},
		{path: "release/a/1", name: "1", parent: "release/a", status: history.Succeeded},
		{path: "release/a/2", name: "2", parent: "release/a", status: history.Succeeded},
		{path: "release/b", name: "b", parent: "release", status: history.Running},
	}
	items := flattenTree(buildForest(rows), 3)
	if len(items) > 3 {
		t.Fatalf("got %d items, want <= 3: %+v", len(items), items)
	}
	// "release" and "release/b" (still running) must survive; "release/a"
	// and its children may be folded into one summary line.
	var sawB bool
	for _, it := range items {
		if !it.isSummary && it.node.row.path == "release/b" {
			sawB = true
		}
	}
	if !sawB {
		t.Fatalf("the running row release/b was dropped: %+v", items)
	}
}

func TestFlattenTreeShowsEverythingWhenThereIsRoom(t *testing.T) {
	rows := []row{
		{path: "release", name: "release", parent: "", status: history.Running},
		{path: "release/a", name: "a", parent: "release", status: history.Succeeded},
		{path: "release/b", name: "b", parent: "release", status: history.Running},
	}
	items := flattenTree(buildForest(rows), 0) // no limit
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(items), items)
	}
}
