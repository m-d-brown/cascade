package ui

import (
	"strings"
	"testing"
)

// layout draws Graph's lines as plain text, with each node's name in place
// of its glyph.
func layout(names []string, deps [][]int) string {
	var b strings.Builder
	for _, l := range Graph(deps) {
		if l.Node < 0 {
			b.WriteString(l.Left + "\n")
			continue
		}
		b.WriteString(l.Left + names[l.Node] + l.Right + "\n")
	}
	return b.String()
}

func TestGraph(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes []string
		deps  [][]int
		want  string
	}{{
		name:  "a chain stays in one column",
		nodes: []string{"a", "b", "c"},
		deps:  [][]int{nil, {0}, {1}},
		want:  "a\nb\nc\n",
	}, {
		name:  "fork and merge",
		nodes: []string{"a", "b", "c", "d"},
		deps:  [][]int{nil, {0}, {0}, {1, 2}},
		want:  "a\n├─╮\nb │\n│ c\n├─╯\nd\n",
	}, {
		name:  "a three-way fork",
		nodes: []string{"a", "b", "c", "d"},
		deps:  [][]int{nil, {0}, {0}, {0}},
		want:  "a\n├─┬─╮\nb │ │\n  c │\n    d\n",
	}, {
		name:  "a merge crosses a lane that carries on",
		nodes: []string{"a", "b", "c", "d", "e"},
		deps:  [][]int{nil, nil, nil, {0, 2}, {1}},
		want:  "a\n│ b\n│ │ c\n├─┼─╯\nd │\n  e\n",
	}, {
		name:  "an unrelated node does not start directly beneath a finished one",
		nodes: []string{"a", "b", "c"},
		deps:  [][]int{nil, nil, {1}},
		want:  "a\n  b\n  c\n",
	}, {
		name:  "a dependency that comes later is ignored",
		nodes: []string{"a", "b"},
		deps:  [][]int{{1}, nil},
		want:  "a\n  b\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := layout(tc.nodes, tc.deps); got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
