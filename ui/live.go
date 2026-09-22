package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/m-d-brown/cascade/history"
	"github.com/m-d-brown/cascade/world"
	"golang.org/x/term"
)

// IsTerminal reports whether f is an interactive terminal, and so whether
// the live display can be used.
func IsTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// Live is a [history.Observer] and [world.Prompter] that renders the run as a
// tree of rows, one per call, growing as the run discovers what it needs to
// call: status glyph, name, elapsed time and the call's most recent log
// line, plus inline interactive modals for approval prompts.
//
// There is no upfront list of rows to seed, the way there was for a
// declared graph. A call announces itself only when the workflow's own
// code reaches it, so the tree Live draws is a running account of what the
// run has done so far, not a plan of what it will do.
//
// Create it with [NewLive], run [Live.Start] before the run and [Live.Stop]
// after it.
type Live struct {
	prog        *tea.Program
	done        chan struct{}
	once        sync.Once
	mu          sync.Mutex
	approvedAll bool
}

// NewLive returns a live display writing to out. Interrupting it (q, esc or
// ctrl-c) calls cancel, which should cancel the run's context.
//
// exitWhenDone controls what happens once the run itself is finished: false
// (the default a caller gets by way of [cli.App.ExitWhenDone] being unset)
// leaves the finished tree on screen to browse, the same as ever, when a
// terminal is driving it; true quits the moment the run ends regardless, so
// the process returns control to the shell without a keypress. That is the
// shape a pipeline runner wants, where a hand-authored workflow wants the
// browse.
func NewLive(out io.Writer, in io.Reader, cancel func(), exitWhenDone bool) *Live {
	m := newModel(cancel)
	opts := []tea.ProgramOption{tea.WithOutput(out)}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	} else {
		opts = append(opts, tea.WithoutRenderer())
	}
	m.keepUp = wantKeepUp(in, exitWhenDone)
	return &Live{prog: tea.NewProgram(m, opts...), done: make(chan struct{})}
}

// wantKeepUp decides whether the finished display should hold itself open
// for browsing. A terminal on the input side can drive it; a pipe or a test
// cannot, so the run just ends there either way. exitWhenDone skips the
// browse outright, whatever's driving input.
func wantKeepUp(in io.Reader, exitWhenDone bool) bool {
	if exitWhenDone {
		return false
	}
	f, ok := in.(*os.File)
	return ok && IsTerminal(f)
}

// Start begins rendering.
func (l *Live) Start() {
	go func() {
		defer close(l.done)
		if _, err := l.prog.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "live display:", err)
		}
	}()
}

// Handle implements [history.Observer].
func (l *Live) Handle(e history.Event) { l.prog.Send(eventMsg(e)) }

// PromptApproval implements [world.Prompter].
func (l *Live) PromptApproval(ctx context.Context, req world.Request) (world.Decision, error) {
	l.mu.Lock()
	if l.approvedAll {
		l.mu.Unlock()
		return world.ApproveAll, nil
	}
	l.mu.Unlock()

	respChan := make(chan world.Decision, 1)
	l.prog.Send(approvalReqMsg{req: req, respChan: respChan})

	select {
	case <-ctx.Done():
		return world.AbortRun, ctx.Err()
	case dec := <-respChan:
		if dec == world.ApproveAll {
			l.mu.Lock()
			l.approvedAll = true
			l.mu.Unlock()
		}
		return dec, nil
	}
}

// Wait blocks until the finished display is dismissed by the user. It
// returns at once when the display is not holding itself open (no terminal
// driving it, or the run was cut short), so a caller can always call it
// before [Live.Stop] and let an interactive run linger on its result.
func (l *Live) Wait() { <-l.done }

// Stop tears the display down and waits for the final frame to be written.
func (l *Live) Stop() {
	l.once.Do(func() {
		select {
		case <-l.done:
			return // already gone: the user dismissed it
		default:
		}
		l.prog.Send(stopMsg{})
		select {
		case <-l.done:
		case <-time.After(2 * time.Second):
			l.prog.Kill()
		}
	})
}

type eventMsg history.Event

type stopMsg struct{}

type tickMsg time.Time

type approvalReqMsg struct {
	req      world.Request
	respChan chan world.Decision
}

type pendingApproval struct {
	req      world.Request
	respChan chan world.Decision
}

type row struct {
	path    string
	name    string
	parent  string
	status  history.Status
	message string
	started time.Time
	elapsed time.Duration
}

type model struct {
	rows        []row
	index       map[string]int
	spin        spinner.Model
	started     time.Time
	finished    bool
	aborting    bool
	cancel      func()
	width       int
	height      int
	counts      map[history.Status]int
	approvals   []pendingApproval
	approvedAll bool

	// keepUp holds the display open once the run finishes, for stepping
	// through what it did, instead of tearing it down. Set only when there
	// is a terminal on the other end to drive it.
	keepUp bool
	// browsing is the hand-driven mode: the full tree with a cursor, folding
	// nothing, entered by moving the cursor or by the run finishing with
	// keepUp set.
	browsing bool
	// cursor is the path of the selected row while browsing, "" until the
	// user first moves it.
	cursor string
	// logView is the open log pane, or nil when the tree is on screen.
	logView *logPane
	// logFile is the run's one text log (flow.log), learned from RunStarted.
	logFile string
	// name titles the display, learned from RunStarted; "cascade" if the
	// run has none.
	name string
}

// logPane reads one call's lines out of the run's single text log.
type logPane struct {
	path   string // the call whose lines this shows
	name   string
	file   string // the run's flow.log
	lines  []string
	offset int
	err    error
}

func (p *logPane) reload() {
	if p.file == "" {
		p.lines, p.err = nil, nil
		return
	}
	data, err := os.ReadFile(p.file)
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if ln != "" && combinedLinePath(ln) == p.path {
			lines = append(lines, ln)
		}
	}
	p.lines = lines
}

// combinedLinePath is the call path a flow.log line is tagged with: its
// third whitespace-separated field, after the timestamp and level. Neither
// the timestamp, the level, nor a call path contains a space, so the field
// is unambiguous however the message is spaced.
func combinedLinePath(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 {
		return ""
	}
	return f[2]
}

func newModel(cancel func()) *model {
	s := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 24
	}
	return &model{
		index:  map[string]int{},
		spin:   s,
		cancel: cancel,
		width:  w,
		height: h,
		counts: map[history.Status]int{},
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, tick())
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case approvalReqMsg:
		if m.approvedAll {
			msg.respChan <- world.ApproveAll
			return m, nil
		}
		// An approval needs the screen: leave any log pane or hand-driven
		// browse so the modal is what the keys talk to.
		m.logView = nil
		m.browsing = false
		m.approvals = append(m.approvals, pendingApproval(msg))
		return m, nil

	case tea.KeyMsg:
		if len(m.approvals) > 0 {
			curr := m.approvals[0]
			switch msg.String() {
			case "y", "Y", "enter":
				curr.respChan <- world.ApproveOnce
				m.approvals = m.approvals[1:]
				return m, nil
			case "n", "N":
				curr.respChan <- world.SkipOnce
				m.approvals = m.approvals[1:]
				return m, nil
			case "p", "P":
				curr.respChan <- world.ApprovePermanent
				m.approvals = m.approvals[1:]
				return m, nil
			case "d", "D":
				curr.respChan <- world.DryRunOnce
				m.approvals = m.approvals[1:]
				return m, nil
			case "a", "A":
				m.approvedAll = true
				for _, a := range m.approvals {
					a.respChan <- world.ApproveAll
				}
				m.approvals = nil
				return m, nil
			case "q", "Q", "esc", "ctrl+c":
				for _, a := range m.approvals {
					a.respChan <- world.AbortRun
				}
				m.approvals = nil
				if !m.finished && m.cancel != nil && !m.aborting {
					m.aborting = true
					m.cancel()
				}
				return m, nil
			}
			return m, nil
		}

		return m, m.key(msg.String())

	case tickMsg:
		if m.logView != nil && !m.finished {
			atEnd := m.logView.offset >= m.maxLogOffset()
			m.logView.reload()
			if atEnd {
				m.logView.offset = m.maxLogOffset() // keep following a running log
			}
		}
		if m.finished {
			return m, nil
		}
		now := time.Now()
		for i := range m.rows {
			if m.rows[i].status == history.Running {
				m.rows[i].elapsed = now.Sub(m.rows[i].started)
			}
		}
		return m, tick()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case stopMsg:
		return m, tea.Quit

	case eventMsg:
		return m, m.apply(history.Event(msg))
	}
	return m, nil
}

func (m *model) apply(e history.Event) tea.Cmd {
	switch e.Kind {
	case history.RunStarted:
		m.started = e.Time
		m.logFile = e.LogPath
		m.name = e.Name
		m.rows = m.rows[:0]
		m.index = map[string]int{}
	case history.TaskStarted:
		m.index[e.Path] = len(m.rows)
		m.rows = append(m.rows, row{
			path: e.Path, name: lastSegment(e.Path), parent: e.Parent,
			status: e.Status, started: e.Time, message: startMessage(e.Status),
		})
	case history.TaskLog, history.TaskStatus:
		// A resumed row barely exists long enough to say anything, but a
		// running one narrates as it goes.
		if r := m.row(e.Path); r != nil && r.status == history.Running {
			r.message = e.Message
		}
	case history.TaskFinished:
		if r := m.row(e.Path); r != nil {
			r.status = e.Status
			r.elapsed = e.Result.Duration
			r.message = e.Result.Summary
			m.counts[e.Status]++
		}
	case history.RunFinished:
		m.finished = true
		if m.keepUp && len(m.rows) > 0 {
			// Hold the tree on screen and hand it to the cursor, rather than
			// quitting the moment the run is done. A run with nothing to show
			// just ends.
			m.browsing = true
			if m.cursor == "" {
				if paths := m.navPaths(); len(paths) > 0 {
					m.cursor = paths[0]
				}
			}
			return nil
		}
		return tea.Quit
	}
	return nil
}

func (m *model) row(path string) *row {
	i, ok := m.index[path]
	if !ok {
		return nil
	}
	return &m.rows[i]
}

// key handles a keystroke once approvals are out of the way: scrolling a log
// pane, or moving the cursor through the tree and opening logs.
func (m *model) key(s string) tea.Cmd {
	if m.logView != nil {
		return m.keyLog(s)
	}
	switch s {
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "enter", "right", "l":
		if m.cursor == "" {
			m.moveCursor(1)
		}
		if m.cursor != "" {
			m.openLog(m.cursor)
		}
	case "esc":
		if m.browsing && !m.finished {
			m.browsing = false // back to just watching the run
			return nil
		}
		return m.quitOrAbort()
	case "q", "ctrl+c":
		return m.quitOrAbort()
	}
	return nil
}

// keyLog handles a keystroke while a log pane is open. Enter pages down,
// then, once the end is in view, moves on to the next step's log.
func (m *model) keyLog(s string) tea.Cmd {
	p := m.logView
	switch s {
	case "esc", "left", "h":
		m.logView = nil
	case "up", "k":
		p.offset = max(p.offset-1, 0)
	case "down", "j":
		p.offset = min(p.offset+1, m.maxLogOffset())
	case "pgup", "b":
		p.offset = max(p.offset-m.logBody(), 0)
	case "pgdown", " ":
		p.offset = min(p.offset+m.logBody(), m.maxLogOffset())
	case "g", "home":
		p.offset = 0
	case "G", "end":
		p.offset = m.maxLogOffset()
	case "enter":
		if p.offset < m.maxLogOffset() {
			p.offset = min(p.offset+m.logBody(), m.maxLogOffset())
			return nil
		}
		paths := m.navPaths()
		if i := indexOf(paths, p.path); i >= 0 && i+1 < len(paths) {
			m.cursor = paths[i+1]
			m.openLog(m.cursor)
		}
	case "q", "ctrl+c":
		return m.quitOrAbort()
	}
	return nil
}

func (m *model) quitOrAbort() tea.Cmd {
	if m.finished {
		return tea.Quit
	}
	if m.cancel != nil && !m.aborting {
		m.aborting = true
		m.cancel()
	}
	return nil
}

// navPaths is every row's path in the order the browse view draws them:
// the sequence the cursor steps along.
func (m *model) navPaths() []string {
	items := flattenAll(buildForest(m.rows))
	paths := make([]string, 0, len(items))
	for _, it := range items {
		if !it.isSummary && it.node != nil {
			paths = append(paths, it.node.row.path)
		}
	}
	return paths
}

// moveCursor steps the selection by d rows, entering browse mode and
// clamping at the ends.
func (m *model) moveCursor(d int) {
	paths := m.navPaths()
	if len(paths) == 0 {
		return
	}
	m.browsing = true
	i := indexOf(paths, m.cursor)
	switch {
	case i < 0 && d < 0:
		i = len(paths) - 1
	case i < 0:
		i = 0
	default:
		i = min(max(i+d, 0), len(paths)-1)
	}
	m.cursor = paths[i]
}

// openLog reads path's lines out of the run's text log into a pane,
// positioned at the top.
func (m *model) openLog(path string) {
	r := m.row(path)
	if r == nil {
		return
	}
	p := &logPane{path: path, name: r.name, file: m.logFile}
	p.reload()
	m.logView = p
}

// logBody is how many log lines fit on screen: all but the two-line header
// and the footer.
func (m *model) logBody() int {
	if m.height <= 0 {
		return len(m.logView.lines)
	}
	return max(m.height-4, 1)
}

// maxLogOffset is the furthest the log pane can scroll: the offset that
// puts the last line at the bottom of the body.
func (m *model) maxLogOffset() int {
	if m.logView == nil {
		return 0
	}
	return max(len(m.logView.lines)-m.logBody(), 0)
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// indexOfItem finds the drawn row for path, or 0 if it is not on screen.
func indexOfItem(items []renderedItem, path string) int {
	for i, it := range items {
		if !it.isSummary && it.node != nil && it.node.row.path == path {
			return i
		}
	}
	return 0
}

func lastSegment(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func startMessage(s history.Status) string {
	switch s {
	case history.Resumed:
		return "resumed"
	case history.Canceled:
		return "canceled"
	default:
		return "running"
	}
}

var (
	dimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	nameStyle         = lipgloss.NewStyle().Bold(true)
	headerStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	approveBadgeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("3")).Padding(0, 1)
	taskBadgeStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	promptKeyStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
	promptMutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cursorStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))

	statusStyle = map[history.Status]lipgloss.Style{
		history.Succeeded: lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		history.Resumed:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		history.Skipped:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		history.Failed:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		history.Canceled:  lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		history.Running:   lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
	}
)

func (m *model) View() string {
	if len(m.rows) == 0 {
		return ""
	}
	if m.logView != nil {
		return m.viewLog()
	}
	if m.browsing {
		return m.viewBrowse()
	}
	return m.viewLive()
}

// header is the one line every view opens with.
func (m *model) header(suffix string) string {
	elapsed := time.Duration(0)
	if !m.started.IsZero() {
		elapsed = time.Since(m.started)
	}
	name := m.name
	if name == "" {
		name = "cascade"
	}
	head := fmt.Sprintf("%s · %d calls · %s", name, len(m.rows), round(elapsed))
	switch {
	case m.aborting:
		head += " · aborting…"
	case suffix != "":
		head += " · " + suffix
	}
	return headerStyle.Render(head)
}

// viewLive is the run as it happens: the folding, self-scrolling tree.
func (m *model) viewLive() string {
	var b strings.Builder
	b.WriteString(m.header(""))
	b.WriteString("\n")

	maxTreeRows := 0
	if m.height > 0 {
		overhead := 2 // header (1) + margin (1)
		if len(m.approvals) > 0 {
			curr := m.approvals[0].req
			overhead += 4
			targetWidth := m.width - 6
			if targetWidth < 40 {
				targetWidth = 40
			}
			wrappedTarget := lipgloss.NewStyle().Width(targetWidth).Render(curr.Target)
			overhead += strings.Count(wrappedTarget, "\n") + 1
		} else if !m.finished {
			overhead++
		}
		maxTreeRows = m.height - overhead
		if maxTreeRows < 3 {
			maxTreeRows = 3
		}
	}

	items := flattenTree(buildForest(m.rows), maxTreeRows)
	nameWidth := nameWidthOf(items)
	for _, it := range items {
		b.WriteString(m.renderItem(it, nameWidth, m.width))
		b.WriteString("\n")
	}

	switch {
	case len(m.approvals) > 0:
		m.writeApproval(&b)
	case m.finished:
		// keepUp is off, so this is the last frame; nothing more to say.
	case m.keepUp:
		b.WriteString(dimStyle.Render("↑/↓ step through · enter logs · q abort"))
		b.WriteString("\n")
	default:
		b.WriteString(dimStyle.Render("press q to abort"))
		b.WriteString("\n")
	}
	return b.String()
}

// viewBrowse is the hand-driven tree: every row, folding nothing, a cursor
// the user moves, windowed to the terminal.
func (m *model) viewBrowse() string {
	var b strings.Builder
	suffix := "browsing — esc to watch"
	if m.finished {
		suffix = "done"
	}
	b.WriteString(m.header(suffix))
	b.WriteString("\n")

	items := flattenAll(buildForest(m.rows))
	nameWidth := nameWidthOf(items)

	body := m.height - 3 // header, footer, one for slack
	if body < 3 || m.height == 0 {
		body = len(items)
	}
	cur := indexOfItem(items, m.cursor)
	start := cur - body/2
	if start > len(items)-body {
		start = len(items) - body
	}
	if start < 0 {
		start = 0
	}
	end := start + body
	if end > len(items) {
		end = len(items)
	}

	if start > 0 {
		fmt.Fprintf(&b, "%s\n", dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	for i := start; i < end; i++ {
		marker := "  "
		if i == cur {
			marker = cursorStyle.Render("▸ ")
		}
		b.WriteString(marker)
		b.WriteString(m.renderItem(items[i], nameWidth, m.width-2))
		b.WriteString("\n")
	}
	if end < len(items) {
		fmt.Fprintf(&b, "%s\n", dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(items)-end)))
	}

	leave := "q abort"
	if m.finished {
		leave = "q quit"
	}
	b.WriteString(dimStyle.Render("↑/↓ move · enter logs · " + leave))
	b.WriteString("\n")
	return b.String()
}

// viewLog is one call's lines from the run's text log, scrolled by hand.
func (m *model) viewLog() string {
	p := m.logView
	var b strings.Builder

	pos := "end"
	if max := m.maxLogOffset(); max > 0 {
		pos = fmt.Sprintf("%d%%", 100*min(p.offset, max)/max)
	} else if len(p.lines) == 0 {
		pos = "—"
	}
	b.WriteString(headerStyle.Render("◂ " + p.name))
	b.WriteString(dimStyle.Render(fmt.Sprintf("   %d lines · %s", len(p.lines), pos)))
	b.WriteString("\n")
	loc := p.file
	if loc == "" {
		loc = "(this run wrote no log)"
	} else {
		loc += "  ·  lines tagged " + p.path
	}
	b.WriteString(truncateVisible(dimStyle.Render(loc), m.width))
	b.WriteString("\n")

	switch {
	case p.err != nil:
		b.WriteString(truncateVisible(statusStyle[history.Failed].Render("cannot read log: "+p.err.Error()), m.width))
		b.WriteString("\n")
	case len(p.lines) == 0:
		b.WriteString(dimStyle.Render("no log lines yet"))
		b.WriteString("\n")
	default:
		if p.offset > m.maxLogOffset() {
			p.offset = m.maxLogOffset()
		}
		end := p.offset + m.logBody()
		if end > len(p.lines) {
			end = len(p.lines)
		}
		for _, ln := range p.lines[p.offset:end] {
			b.WriteString(truncateVisible(ln, m.width))
			b.WriteString("\n")
		}
	}

	b.WriteString(dimStyle.Render("↑/↓ scroll · enter more, then next step · esc back · q quit"))
	b.WriteString("\n")
	return b.String()
}

// writeApproval draws the inline approval modal beneath the tree.
func (m *model) writeApproval(b *strings.Builder) {
	curr := m.approvals[0].req
	count := ""
	if len(m.approvals) > 1 {
		count = fmt.Sprintf(" (1 of %d pending)", len(m.approvals))
	}

	b.WriteString("\n")
	fmt.Fprintf(b, "%s %s%s\n",
		approveBadgeStyle.Render("APPROVE"),
		taskBadgeStyle.Render(curr.Task),
		dimStyle.Render(count),
	)

	fmt.Fprintf(b, "  %s\n", dimStyle.Render("Effect:"))

	// Wrap the target to the terminal so the whole command is visible.
	targetWidth := m.width - 6
	if targetWidth < 40 {
		targetWidth = 40
	}
	wrappedTarget := lipgloss.NewStyle().Width(targetWidth).Foreground(lipgloss.Color("14")).Render(curr.Target)
	for _, line := range strings.Split(wrappedTarget, "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "  Execute? %s  %s  %s  %s  %s  %s\n",
		promptKeyStyle.Render("[y]es"),
		promptMutedStyle.Render("[n]o (skip)"),
		promptMutedStyle.Render("[p]ermanent allow"),
		promptMutedStyle.Render("[d]ry-run"),
		promptMutedStyle.Render("[a]pprove all"),
		promptMutedStyle.Render("[q]uit"),
	)
}

// renderItem formats one tree line, the same row shape the live and browse
// views both draw, clipped to width.
func (m *model) renderItem(it renderedItem, nameWidth, width int) string {
	if it.isSummary {
		return truncateVisible(dimStyle.Render(it.summary), width)
	}
	r := *it.node.row
	glyph := r.status.Symbol()
	if r.status == history.Running {
		glyph = m.spin.View()
	}
	style := statusStyle[r.status]

	var line string
	if it.prefix == "" {
		line = fmt.Sprintf("%s %s %s %s",
			style.Render(glyph),
			nameStyle.Render(pad(r.name, nameWidth)),
			dimStyle.Render(pad(elapsedText(r), 7)),
			dimStyle.Render(r.message),
		)
	} else {
		padSpaces := nameWidth - lipgloss.Width(it.prefix) - lipgloss.Width(r.name)
		if padSpaces < 0 {
			padSpaces = 0
		}
		line = fmt.Sprintf("%s%s %s%s %s %s",
			dimStyle.Render(it.prefix),
			style.Render(glyph),
			nameStyle.Render(r.name),
			strings.Repeat(" ", padSpaces),
			dimStyle.Render(pad(elapsedText(r), 7)),
			dimStyle.Render(r.message),
		)
	}
	return truncateVisible(line, width)
}

func nameWidthOf(items []renderedItem) int {
	nameWidth := 4
	for _, it := range items {
		if !it.isSummary && it.node != nil {
			if w := lipgloss.Width(it.prefix) + lipgloss.Width(it.node.row.name); w > nameWidth {
				nameWidth = w
			}
		}
	}
	return nameWidth
}

func elapsedText(r row) string {
	return round(r.elapsed).String()
}

func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// truncateVisible cuts a styled line to width, ignoring ANSI escapes.
func truncateVisible(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	visible, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && (r == 'm' || r == 'K'):
			inEscape = false
		case !inEscape:
			if visible >= width-1 {
				b.WriteString("…\x1b[0m")
				return b.String()
			}
			visible++
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sortedStatuses(counts map[history.Status]int) []history.Status {
	out := make([]history.Status, 0, len(counts))
	for s := range counts {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
