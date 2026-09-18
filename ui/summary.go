package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mdbrown/cascade/history"
)

var (
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("1")).
			Padding(0, 1)
	verdictOK   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
	verdictBad  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	headerCell  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Padding(0, 1)
	regularCell = lipgloss.NewStyle().Padding(0, 1)
)

// Summary writes the end-of-run report: a table of every call, then a panel
// per failure, then the verdict.
func Summary(w io.Writer, res *history.Result) {
	rows := res.SortedResults()
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("8"))).
		Headers("", "TASK", "STATUS", "TIME", "RESULT").
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerCell
			}
			if col == 2 && row < len(rows) {
				if style, ok := statusStyle[rows[row].Status]; ok {
					return style.Padding(0, 1)
				}
			}
			return regularCell
		})
	for _, r := range rows {
		style := statusStyle[r.Status]
		t.Row(
			style.Render(r.Status.Symbol()),
			r.Path,
			r.Status.String(),
			round(r.Duration).String(),
			truncate(r.Summary, 60),
		)
	}
	fmt.Fprintln(w, t.Render())

	for _, f := range res.Failed() {
		body := f.Err.Error()
		if f.LogPath != "" {
			body += "\n\nlog: " + f.LogPath + "  (lines tagged " + f.Path + ")"
		}
		fmt.Fprintln(w, panelStyle.Render(lipgloss.NewStyle().Bold(true).Render(f.Path)+"\n"+body))
	}

	var parts []string
	for _, s := range sortedStatuses(res.Counts) {
		if res.Counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", res.Counts[s], s))
		}
	}
	verdict := verdictOK.Render("✓ all calls succeeded")
	if res.ExitCode() != 0 {
		verdict = verdictBad.Render("✗ run failed")
	}
	if res.Aborted {
		verdict = verdictBad.Render("✗ run aborted: " + res.AbortReason)
	}
	fmt.Fprintf(w, "%s  ·  %s  ·  %s\n", verdict, strings.Join(parts, ", "), round(res.Duration))
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
