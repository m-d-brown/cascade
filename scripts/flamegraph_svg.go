//go:build ignore

// Command flamegraph_svg renders a run's Chrome Trace Event Format JSON
// (history.Flamegraph's output, what `release flamegraph` prints) as a
// static SVG concurrency timeline: one row per call, a bar from its start
// to its finish, colored by status. chrome://tracing and
// https://ui.perfetto.dev are the real tools for exploring a trace, with
// zoom and per-span times; this is only enough to put a picture in a doc
// without asking a reader to drag a file into a browser first.
//
//	go run scripts/flamegraph_svg.go trace.json > flamegraph.svg
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type traceFile struct {
	TraceEvents []struct {
		Name string         `json:"name"`
		Ph   string         `json:"ph"`
		TS   int64          `json:"ts"`
		Dur  int64          `json:"dur"`
		TID  int            `json:"tid"`
		Args map[string]any `json:"args"`
	} `json:"traceEvents"`
}

type span struct {
	name    string
	ts, dur int64
	status  string
}

var statusColor = map[string]string{
	"ok":       "#2e7d32",
	"resumed":  "#00838f",
	"skipped":  "#9e9e9e",
	"failed":   "#c62828",
	"canceled": "#757575",
	"running":  "#1565c0",
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: flamegraph_svg trace.json > out.svg")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var tf traceFile
	if err := json.Unmarshal(data, &tf); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	lanes := map[int]string{}
	var title string
	var spans []span
	for _, e := range tf.TraceEvents {
		switch {
		case e.Ph == "M" && e.Name == "thread_name":
			if n, ok := e.Args["name"].(string); ok {
				lanes[e.TID] = n
			}
		case e.Ph == "M" && e.Name == "process_name":
			if n, ok := e.Args["name"].(string); ok {
				title = n
			}
		case e.Ph == "X":
			status, _ := e.Args["status"].(string)
			spans = append(spans, span{name: e.Name, ts: e.TS, dur: e.Dur, status: status})
		}
	}
	if len(spans) == 0 {
		fmt.Fprintln(os.Stderr, "error: no spans in trace")
		os.Exit(1)
	}

	tids := make([]int, 0, len(lanes))
	for t := range lanes {
		tids = append(tids, t)
	}
	sort.Ints(tids)
	rowOf := make(map[string]int, len(tids))
	for i, t := range tids {
		rowOf[lanes[t]] = i
	}

	var maxEnd int64
	for _, s := range spans {
		if e := s.ts + s.dur; e > maxEnd {
			maxEnd = e
		}
	}

	const (
		rowH      = 24
		barH      = 14
		labelW    = 300
		chartW    = 640
		leftPad   = 14
		rightPad  = 90
		topPad    = 40
		bottomPad = 14
	)
	rows := len(tids)
	width := leftPad + labelW + chartW + rightPad
	height := topPad + rows*rowH + bottomPad

	scale := func(us int64) float64 {
		if maxEnd == 0 {
			return 0
		}
		return float64(us) / float64(maxEnd) * float64(chartW)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="SFMono-Regular,Menlo,Consolas,monospace">`+"\n",
		width, height, width, height)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`+"\n", width, height)
	if title != "" {
		fmt.Fprintf(&b, `<text x="%d" y="20" font-size="13" fill="#212121">%s — concurrency timeline</text>`+"\n",
			leftPad, esc(title))
	}

	chartX := leftPad + labelW
	axisY := topPad - 10
	step := niceStep(float64(maxEnd) / 1e6)
	for t := 0.0; t*1e6 <= float64(maxEnd)+1; t += step {
		x := float64(chartX) + scale(int64(t*1e6))
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#e0e0e0" stroke-width="1"/>`+"\n",
			x, topPad-4, x, height-bottomPad)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#757575" text-anchor="middle">%s</text>`+"\n",
			x, axisY, formatSeconds(t))
	}

	for _, s := range spans {
		row, ok := rowOf[s.name]
		if !ok {
			continue
		}
		y := topPad + row*rowH
		barX := float64(chartX) + scale(s.ts)
		barW := scale(s.dur)
		if barW < 2 {
			barW = 2
		}
		color := statusColor[s.status]
		if color == "" {
			color = "#616161"
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="10.5" fill="#212121" text-anchor="end">%s</text>`+"\n",
			chartX-8, y+rowH/2+4, esc(s.name))
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" rx="3" fill="%s"><title>%s (%s)</title></rect>`+"\n",
			barX, y+(rowH-barH)/2, barW, barH, color, esc(s.name), esc(s.status))
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="9.5" fill="#757575">%s</text>`+"\n",
			barX+barW+6, y+rowH/2+4, formatDuration(s.dur))
	}

	b.WriteString(`</svg>` + "\n")
	fmt.Print(b.String())
}

// niceStep picks a gridline interval, in seconds, aiming for roughly five
// gridlines across the chart.
func niceStep(totalSeconds float64) float64 {
	steps := []float64{0.1, 0.2, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300}
	target := totalSeconds / 5
	for _, s := range steps {
		if s >= target {
			return s
		}
	}
	return steps[len(steps)-1]
}

func formatSeconds(s float64) string {
	if s == 0 {
		return "0s"
	}
	return fmt.Sprintf("%gs", s)
}

func formatDuration(us int64) string {
	d := time.Duration(us) * time.Microsecond
	switch {
	case d == 0:
		return "0s"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
