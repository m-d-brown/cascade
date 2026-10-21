package ui

import (
	"fmt"

	"github.com/m-d-brown/cascade/history"
)

type treeNode struct {
	row      *row
	parent   *treeNode
	children []*treeNode
}

// isRunning reports whether a row is currently executing. Failed and
// Canceled are terminal: a subtree containing one of those is done making
// noise even though something in it went wrong, which is why they do not
// keep a subtree from folding the way a still-Running row does.
func isRunning(r *row) bool { return r.status == history.Running }

// subtreeRunning reports whether a node or any of its descendants is
// currently running, which is what decides whether the subtree can be
// folded away: there is nothing left to watch in it right now.
func subtreeRunning(n *treeNode) bool {
	if isRunning(n.row) {
		return true
	}
	for _, c := range n.children {
		if subtreeRunning(c) {
			return true
		}
	}
	return false
}

// countSubtree returns the total number of rows in the subtree rooted at n.
func countSubtree(n *treeNode) int {
	cnt := 1
	for _, c := range n.children {
		cnt += countSubtree(c)
	}
	return cnt
}

// buildForest turns the run's rows into a forest: one tree per top-level
// call, each row hanging from the row named as its parent when it started.
//
// Unlike a graph inferred from declared dependencies, this is exact: a call
// has exactly one parent, whichever call made it, so there is nothing to
// pick among and no depth to infer. Children appear in the order their
// calls started, which for a run in progress is also the order that is
// useful to read.
func buildForest(rows []row) []*treeNode {
	if len(rows) == 0 {
		return nil
	}
	nodes := make([]*treeNode, len(rows))
	byPath := make(map[string]*treeNode, len(rows))
	for i := range rows {
		nodes[i] = &treeNode{row: &rows[i]}
		byPath[rows[i].path] = nodes[i]
	}
	var roots []*treeNode
	for i := range rows {
		n := nodes[i]
		if parent, ok := byPath[rows[i].parent]; ok && rows[i].parent != "" {
			n.parent = parent
			parent.children = append(parent.children, n)
		} else {
			roots = append(roots, n)
		}
	}
	return roots
}

// renderedItem is one drawn line. A line with neither a node nor a summary
// only joins the lanes of a lane graph, and prefix is all of it.
type renderedItem struct {
	node *treeNode
	// prefix is what comes before the node's status glyph, and right is the
	// lanes of a lane graph that continue to the right of it.
	prefix    string
	right     string
	isSummary bool
	summary   string
}

// how says what becomes of a node when the tree is drawn.
type how int

const (
	drawRow  how = iota // draw the node's own row
	collapse            // draw one line standing in for the node and all beneath it
	omit                // draw nothing: a collapsed ancestor already speaks for it
)

// How much detail the drawing gives up. A terminal too short for the first
// level reaches the second.
const (
	foldInactive   = iota // fold every subtree with nothing running in it
	keepActiveOnly        // keep only what is running, and what it hangs from
)

// flattenTree turns the forest into the lines to print, in at most maxRows
// of them. maxRows of zero or less means no limit.
//
// What to leave out is decided before anything is drawn, because the two
// cannot be settled in one pass: a "├─" is a promise that another row
// follows at the same level, and only the set of rows that survive can say
// whether one does. Deciding while drawing leaves connectors pointing at
// rows that were then dropped, and drops rows that no summary line accounts
// for.
func flattenTree(roots []*treeNode, maxRows int) []renderedItem {
	for level := foldInactive; ; level++ {
		items := drawForest(roots, decide(roots, level))
		if maxRows <= 0 || len(items) <= maxRows || level == keepActiveOnly {
			return capRows(items, maxRows)
		}
	}
}

// flattenAll renders every row in the forest and folds nothing. This is the
// shape used when the user is stepping through the tree by hand rather than
// watching it unfold. drawForest reads a nil plan as "draw every node".
func flattenAll(roots []*treeNode) []renderedItem {
	return drawForest(roots, nil)
}

// decide plans what becomes of every node in the forest.
func decide(roots []*treeNode, level int) map[*treeNode]how {
	plan := map[*treeNode]how{}

	var fold func(n *treeNode)
	fold = func(n *treeNode) {
		if !subtreeRunning(n) && countSubtree(n) > 1 {
			markSubtree(n, plan, omit)
			plan[n] = collapse // the head is the line that speaks for the rest
			return
		}
		for _, c := range n.children {
			fold(c)
		}
	}
	for _, r := range roots {
		fold(r)
	}

	if level < keepActiveOnly {
		return plan
	}

	// Last resort: the rows that are running, and the rows they hang from.
	// Keeping the ancestors is what makes this still a tree, rather than a
	// list with holes where its parents used to be.
	keep := map[*treeNode]bool{}
	var mark func(n *treeNode)
	mark = func(n *treeNode) {
		if isRunning(n.row) {
			for p := n; p != nil; p = p.parent {
				keep[p] = true
			}
		}
		for _, c := range n.children {
			mark(c)
		}
	}
	var sweep func(n *treeNode)
	sweep = func(n *treeNode) {
		if !keep[n] {
			plan[n] = omit
		} else if plan[n] == collapse {
			plan[n] = drawRow
		}
		for _, c := range n.children {
			sweep(c)
		}
	}
	for _, r := range roots {
		mark(r)
	}
	for _, r := range roots {
		sweep(r)
	}
	return plan
}

func markSubtree(n *treeNode, plan map[*treeNode]how, v how) {
	plan[n] = v
	for _, c := range n.children {
		markSubtree(c, plan, v)
	}
}

// drawForest renders the planned nodes, giving each the connectors that
// match the rows actually drawn beside it.
//
// Siblings are drawn with tree connectors, unless some of them declared
// with work.After that they waited on others. Those siblings are drawn as a
// lane graph instead (see [Graph]), so a chain of dependencies stays in one
// column rather than stepping further right with every link.
func drawForest(roots []*treeNode, plan map[*treeNode]how) []renderedItem {
	var out []renderedItem
	var group func(nodes []*treeNode, indent string, top bool)
	// draw emits n, then everything beneath it, which below prefixes.
	draw := func(n *treeNode, prefix, right, below string) {
		if plan[n] == collapse {
			out = append(out, renderedItem{node: n, prefix: prefix, right: right,
				isSummary: true, summary: summaryOf(n)})
			return
		}
		out = append(out, renderedItem{node: n, prefix: prefix, right: right})
		group(n.children, below, false)
	}
	group = func(nodes []*treeNode, indent string, top bool) {
		visible := make([]*treeNode, 0, len(nodes))
		for _, n := range nodes {
			if plan[n] != omit {
				visible = append(visible, n)
			}
		}
		if deps := siblingDeps(visible); deps != nil {
			for _, l := range Graph(deps) {
				if l.Node < 0 {
					out = append(out, renderedItem{prefix: indent + l.Left})
					continue
				}
				draw(visible[l.Node], indent+l.Left, l.Right, indent+l.Below+"  ")
			}
			return
		}
		for i, n := range visible {
			switch {
			case top: // a root hangs from nothing
				draw(n, "", "", "  ")
			case i == len(visible)-1:
				draw(n, indent+"└─ ", "", indent+"   ")
			default:
				draw(n, indent+"├─ ", "", indent+"│  ")
			}
		}
	}
	group(roots, "", true)

	if hidden := countHidden(roots, plan); hidden > 0 {
		out = append(out, renderedItem{isSummary: true,
			summary: fmt.Sprintf("… (%d more, none of them running)", hidden)})
	}
	return out
}

// countHidden counts the nodes left out that no collapsed line speaks for.
func countHidden(roots []*treeNode, plan map[*treeNode]how) int {
	var n int
	var walk func(node *treeNode)
	walk = func(node *treeNode) {
		switch plan[node] {
		case collapse:
			return // its own line already says how many are under it
		case omit:
			n++
		}
		for _, c := range node.children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return n
}

// summaryOf is the one line that stands in for a node and everything under it.
func summaryOf(n *treeNode) string {
	count := countSubtree(n)
	glyph := statusStyle[n.row.status].Render(n.row.status.Symbol())
	return fmt.Sprintf("%s %s … (%d calls %s)", glyph, n.row.name, count, n.row.status)
}

// capRows cuts the drawn lines to the budget, saying how many did not fit.
func capRows(items []renderedItem, maxRows int) []renderedItem {
	if maxRows <= 0 || len(items) <= maxRows {
		return items
	}
	left := len(items) - (maxRows - 1)
	out := append([]renderedItem(nil), items[:maxRows-1]...)
	return append(out, renderedItem{isSummary: true,
		summary: fmt.Sprintf("… (%d more)", left)})
}

// siblingDeps is, for each of the siblings, the indices of the others it
// declared it waited on, or nil if none of them waited on another. A
// dependency that is not among them (one folded away, or never started) has
// no line to draw from, so it is left out.
func siblingDeps(siblings []*treeNode) [][]int {
	index := make(map[string]int, len(siblings))
	for i, n := range siblings {
		index[n.row.path] = i
	}
	deps := make([][]int, len(siblings))
	any := false
	for i, n := range siblings {
		for _, a := range n.row.after {
			if j, ok := index[a]; ok {
				deps[i] = append(deps[i], j)
				any = true
			}
		}
	}
	if !any {
		return nil
	}
	return deps
}
