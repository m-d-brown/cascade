package ui

import (
	"slices"
	"strings"
)

// GraphLine is one line of a lane graph laid out by [Graph].
type GraphLine struct {
	// Node is the index of the node this line draws, or -1 for a line that
	// only joins lanes: the merge above a node with several dependencies, or
	// the fork below a node with several dependents.
	Node int
	// Left and Right are the lanes on either side of the node's glyph, which
	// the caller draws between them. A line that only joins lanes is all
	// Left.
	Left, Right string
	// Below is the lanes that continue past the node, for prefixing any
	// lines the caller draws beneath it before the graph's next line.
	Below string
}

// Graph lays out nodes and the dependencies between them as a lane graph, in
// the style of `git log --graph`: one line per node in the order given, with
// lanes beside it carrying each dependency down to the node that waited on
// it. deps[i] lists the nodes that node i depends on; a dependency that does
// not come earlier in the order is ignored.
//
//	✓ deps
//	├─╮
//	✓ │ compile
//	│ ✓ lint
//	✓ │ test
//	├─╯
//	✓ package
//
// The width grows with how many dependencies are outstanding at once, not
// with the length of a chain of them.
func Graph(deps [][]int) []GraphLine {
	dependents := make([][]int, len(deps))
	for j, ds := range deps {
		for _, d := range ds {
			if d >= 0 && d < j && !slices.Contains(dependents[d], j) {
				dependents[d] = append(dependents[d], j)
			}
		}
	}

	var (
		lanes []int // the node each lane's edge leads to, or -1 for a free lane
		out   []GraphLine
		// freed is the lane the node just drawn left free. The next node
		// does not start a new lane there: directly beneath, it would read as
		// that node's dependent.
		freed = -1
	)
	for j := range deps {
		var in []int
		for l, t := range lanes {
			if t == j {
				in = append(in, l)
			}
		}
		var c int
		if len(in) == 0 {
			lanes, c = claim(lanes, 0, freed)
		} else {
			c = in[0]
			if len(in) > 1 {
				out = append(out, GraphLine{Node: -1, Left: join(lanes, c, in[1:], '╯', '┴')})
				for _, l := range in[1:] {
					lanes[l] = -1
				}
			}
		}

		lanes[c] = j
		row := cells(lanes)
		line := GraphLine{Node: j, Left: string(row[:2*c]), Right: trim(row[2*c+1:])}

		next := dependents[j]
		freed = -1
		if len(next) == 0 {
			lanes[c] = -1
			freed = c
		} else {
			lanes[c] = next[0]
		}
		line.Below = trim(cells(lanes))
		out = append(out, line)

		if len(next) > 1 {
			forked := make([]int, 0, len(next)-1)
			for _, t := range next[1:] {
				var l int
				lanes, l = claim(lanes, c+1, -1)
				lanes[l] = t
				forked = append(forked, l)
			}
			out = append(out, GraphLine{Node: -1, Left: join(lanes, c, forked, '╮', '┬')})
		}
		for len(lanes) > 0 && lanes[len(lanes)-1] < 0 {
			lanes = lanes[:len(lanes)-1]
		}
	}
	return out
}

// claim returns the first free lane at or after from, other than avoid,
// adding lanes as needed.
func claim(lanes []int, from, avoid int) ([]int, int) {
	for l := from; ; l++ {
		if l == avoid {
			continue
		}
		for l >= len(lanes) {
			lanes = append(lanes, -1)
		}
		if lanes[l] < 0 {
			return lanes, l
		}
	}
}

// cells draws the lanes as one row: a vertical line for each lane carrying
// an edge, a space for each free one, and a space after each.
func cells(lanes []int) []rune {
	row := make([]rune, 2*len(lanes))
	for l, t := range lanes {
		row[2*l], row[2*l+1] = ' ', ' '
		if t >= 0 {
			row[2*l] = '│'
		}
	}
	return row
}

// join draws a row in which the lanes in ends meet lane c with a horizontal
// line: last at the farthest of them, mid at the others, and a crossing
// wherever the line passes over a lane that carries on.
func join(lanes []int, c int, ends []int, last, mid rune) string {
	row := cells(lanes)
	hi := slices.Max(ends)
	row[2*c] = '├'
	for p := 2*c + 1; p < 2*hi; p++ {
		switch l := p / 2; {
		case p%2 == 1:
			row[p] = '─'
		case slices.Contains(ends, l):
			row[p] = mid
		case lanes[l] >= 0:
			row[p] = '┼'
		default:
			row[p] = '─'
		}
	}
	row[2*hi] = last
	return trim(row)
}

func trim(row []rune) string { return strings.TrimRight(string(row), " ") }
