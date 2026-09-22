package history

import (
	"fmt"
	"sort"
	"strings"

	"github.com/emicklei/dot"
)

// DotOptions controls how [Dot] renders a run's trace.
type DotOptions struct {
	// Title is the graph label.
	Title string
	// Cluster groups calls by their first tag.
	Cluster bool
	// LeftToRight lays the graph out horizontally.
	LeftToRight bool
}

var statusColor = map[Status]string{
	Succeeded: "#2e7d32",
	Resumed:   "#00838f",
	Skipped:   "#9e9e9e",
	Failed:    "#c62828",
	Canceled:  "#757575",
	Running:   "#1565c0",
}

// Dot renders a run's trace in Graphviz DOT format: one box per call it
// made, nested under the call that made it.
//
// There is no graph to draw before a run happens: what a workflow calls is
// exactly what its own code decides to call, which is not known until it
// runs. Dot draws what a run *did*: pass a run that finished for a complete
// picture, or one still in progress, or a run executed against a dry-run
// world purely to see its shape, for a partial one.
//
//	res, _ := runner.Run(ctx)
//	fmt.Println(history.Dot(journalRunOf(res), history.DotOptions{Title: "release"}))
func Dot(r Run, opts DotOptions) string {
	d := dot.NewGraph(dot.Directed)
	if opts.Title != "" {
		d.Attr("label", opts.Title)
		d.Attr("labelloc", "t")
		d.Attr("fontsize", "18")
	}
	if opts.LeftToRight {
		d.Attr("rankdir", "LR")
	}
	d.Attr("nodesep", "0.4")
	d.Attr("ranksep", "0.6")
	d.NodeInitializer(func(n dot.Node) {
		n.Attr("shape", "box")
		n.Attr("style", "rounded,filled")
		n.Attr("fontname", "Helvetica")
		n.Attr("fillcolor", "white")
	})
	d.EdgeInitializer(func(e dot.Edge) {
		e.Attr("fontname", "Helvetica")
		e.Attr("fontsize", "9")
	})

	paths := make([]string, 0, len(r.Tasks))
	for p := range r.Tasks {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	clusters := map[string]*dot.Graph{}
	container := func(rec Record) *dot.Graph {
		if !opts.Cluster || rec.Tag == "" {
			return d
		}
		tag := rec.Tag
		if sub, ok := clusters[tag]; ok {
			return sub
		}
		sub := d.Subgraph(tag, dot.ClusterOption{})
		sub.Attr("style", "rounded")
		sub.Attr("color", "#bdbdbd")
		sub.Attr("fontname", "Helvetica")
		clusters[tag] = sub
		return sub
	}

	nodes := make(map[string]dot.Node, len(paths))
	for _, p := range paths {
		rec := r.Tasks[p]
		dn := container(rec).Node(p)
		dn.Attr("label", nodeLabel(p, rec))
		if color, ok := statusColor[rec.Status]; ok {
			dn.Attr("color", color)
			dn.Attr("penwidth", "2")
			dn.Attr("fontcolor", color)
		}
		nodes[p] = dn
	}
	for _, p := range paths {
		parent := parentOf(p)
		if parent == "" {
			continue
		}
		from, ok := nodes[parent]
		if !ok {
			continue
		}
		d.Edge(from, nodes[p])
	}

	return d.String()
}

func nodeLabel(path string, rec Record) string {
	var b strings.Builder
	b.WriteString(lastSegment(path))
	if rec.Status != Pending {
		fmt.Fprintf(&b, "  [%s]", rec.Status)
	}
	if rec.Doc != "" {
		fmt.Fprintf(&b, "\n%s", truncate(rec.Doc, 48))
	}
	return b.String()
}

func lastSegment(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func parentOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
