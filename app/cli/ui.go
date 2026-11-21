package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The interactive terminal UI. It shows what the backend records, through
// the same helpers the text commands use, and its actions call the same
// functions the commands do; it decides nothing itself.

// uiRow is one line of a page. Rows with open are selectable.
type uiRow struct {
	text   string // may hold styling
	open   func() (*uiPage, error)
	target uiTarget // what actions on this row act on
}

// uiTarget is what a row or page is about, for the actions that act on
// it. At most one of file, contract, and run is set.
type uiTarget struct {
	file     string // a contract file in assay/
	contract string // a published contract
	version  int    // with contract: one version, or 0 for the latest
	run      string
}

// uiPage is one screen: a title for the breadcrumb and its lines.
type uiPage struct {
	title  string
	rows   []uiRow
	cursor int // index of the selected row, or -1 when nothing is selectable
	offset int // first visible row
	// build makes the page again from the database, for reloading.
	build func() (*uiPage, error)
	// target is what the page is about, for actions when the selected row
	// isn't about anything.
	target uiTarget
}

func (p *uiPage) about(target uiTarget) *uiPage {
	p.target = target
	return p
}

func newPage(title string, rows []uiRow, build func() (*uiPage, error)) *uiPage {
	p := &uiPage{title: title, rows: rows, cursor: -1, build: build}
	p.cursor = p.next(-1, 1)
	return p
}

// next finds the selectable row after (dir 1) or before (dir -1) from.
func (p *uiPage) next(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(p.rows); i += dir {
		if p.rows[i].open != nil {
			return i
		}
	}
	return -1
}

const (
	tabContracts = iota
	tabRuns
)

var tabNames = []string{"Contracts", "Runs"}

type uiModel struct {
	data   *uiData
	tab    int
	stack  []*uiPage // stack[0] is the tab's list
	width  int
	height int
	help   bool
	status string // the last error, shown above the keys
	note   string // the last action's outcome, shown in the same place
	// busy describes a check that is running, while one is; actions wait
	// for it to finish.
	busy string
	// confirm is the question the current page asks, such as whether to
	// publish the diff it shows.
	confirm *uiConfirm
	// welcome is the panel shown when the UI opens, until the first time
	// something is opened or the tab changes.
	welcome *uiWelcome
}

// uiWelcome is what the welcome panel says.
type uiWelcome struct {
	version string
	project string // the project's root, with ~ for the home directory
	summary string // e.g. "2 contracts · 3 runs · last run passed"
}

func newUIModel(data *uiData, tab int) *uiModel {
	m := &uiModel{data: data, tab: tab, width: 80, height: 24}
	m.stack = []*uiPage{m.topPage()}
	return m
}

// welcomeMinHeight is the smallest terminal that shows the welcome panel;
// in a shorter one the lists need every row.
const welcomeMinHeight = 20

func (m *uiModel) showWelcome() bool {
	return m.welcome != nil && !m.help && len(m.stack) == 1 && m.height >= welcomeMinHeight
}

// logo is the name in large letters, for the welcome panel.
var logo = []string{
	" █████╗ ███████╗███████╗ █████╗ ██╗   ██╗",
	"██╔══██╗██╔════╝██╔════╝██╔══██╗╚██╗ ██╔╝",
	"███████║███████╗███████╗███████║ ╚████╔╝ ",
	"██╔══██║╚════██║╚════██║██╔══██║  ╚██╔╝  ",
	"██║  ██║███████║███████║██║  ██║   ██║   ",
	"╚═╝  ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝   ╚═╝   ",
}

// bigLogoMinHeight is the smallest terminal that shows the large logo; a
// shorter one gets the one-line title.
const bigLogoMinHeight = 24

func (m *uiModel) bigLogo() bool {
	return m.height >= bigLogoMinHeight && min(m.width, 64)-4 >= ansi.StringWidth(logo[0])
}

// welcomeLines draws the welcome panel: a rounded box like Claude Code's.
func (m *uiModel) welcomeLines() []string {
	w := m.welcome
	width := min(m.width, 64)
	inner := width - 4 // the border and padding
	var lines []string
	if m.bigLogo() {
		for _, line := range logo {
			lines = append(lines, styleAccent.Render(line))
		}
		lines = append(lines, "", styleTitle.Render("Welcome to assay")+styleDim.Render("  "+w.version))
	} else {
		lines = append(lines, styleAccent.Render("◆")+" "+styleTitle.Render("Welcome to assay")+styleDim.Render("  "+w.version), "")
	}
	lines = append(lines,
		styleDim.Render("  ? for keys · tab for runs · enter to open"),
		styleDim.Render("  project: ")+truncateLeft(w.project, inner-11),
		"  "+w.summary,
	)
	// Cut lines rather than let the box wrap them, so it stays its height.
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, inner, "…")
	}
	box := styleWelcome.Width(width).Render(strings.Join(lines, "\n"))
	return strings.Split(box, "\n")
}

func (m *uiModel) topPage() *uiPage {
	build := m.data.contractsPage
	if m.tab == tabRuns {
		build = m.data.runsPage
	}
	page, err := build()
	if err != nil {
		m.status = err.Error()
		return newPage(tabNames[m.tab], nil, build)
	}
	return page
}

func (m *uiModel) page() *uiPage { return m.stack[len(m.stack)-1] }

func (m *uiModel) Init() tea.Cmd { return nil }

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.keepVisible()
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	case uiDoneMsg:
		m.done(msg)
	case uiEditedMsg:
		m.edited(msg)
	}
	return m, nil
}

// bodyHeight is the number of rows a page has room for: the screen less
// the header, its rule, the status line, and the key line.
func (m *uiModel) bodyHeight() int {
	h := m.height - 4
	if m.showWelcome() {
		h -= len(m.welcomeLines())
	}
	return max(1, h)
}

func (m *uiModel) key(key string) tea.Cmd {
	if key == "ctrl+c" {
		return tea.Quit
	}
	if m.help {
		switch key {
		case "?", "esc", "q", "enter":
			m.help = false
		}
		return nil
	}
	if m.confirm != nil {
		return m.answer(key)
	}
	m.status, m.note = "", ""
	p := m.page()
	switch key {
	case "?":
		m.help = true
	case "q", "esc", "left", "h", "backspace":
		if len(m.stack) == 1 {
			if key == "q" || key == "esc" {
				return tea.Quit
			}
			return nil
		}
		m.stack = m.stack[:len(m.stack)-1]
		m.reload() // an action may have changed what the page shows
	case "tab", "shift+tab":
		m.welcome = nil
		m.tab = (m.tab + 1) % len(tabNames)
		m.stack = []*uiPage{m.topPage()}
	case "1", "2":
		m.welcome = nil
		m.tab = int(key[0] - '1')
		m.stack = []*uiPage{m.topPage()}
	case "enter", "right", "l":
		if p.cursor < 0 {
			return nil
		}
		next, err := p.rows[p.cursor].open()
		if err != nil {
			m.status = err.Error()
			return nil
		}
		m.welcome = nil
		m.stack = append(m.stack, next)
	case "ctrl+r":
		m.reload()
	case "v", "p", "e", "c", "r":
		return m.act(key)
	case "down", "j":
		m.move(1)
	case "up", "k":
		m.move(-1)
	case "pgdown", "space", "ctrl+d", "f":
		m.scroll(m.bodyHeight())
	case "pgup", "ctrl+u", "b":
		m.scroll(-m.bodyHeight())
	case "home", "g":
		p.offset = 0
		p.cursor = p.next(-1, 1)
	case "end", "G":
		p.offset = max(0, len(p.rows)-m.bodyHeight())
		p.cursor = p.next(len(p.rows), -1)
	}
	m.keepVisible()
	return nil
}

// move selects the next selectable row, or scrolls when there is none.
func (m *uiModel) move(dir int) {
	p := m.page()
	if p.cursor >= 0 {
		if next := p.next(p.cursor, dir); next >= 0 {
			p.cursor = next
			// Show the lines that introduce the first row, and the lines
			// that follow the last one.
			if dir < 0 && p.next(next, -1) < 0 {
				p.offset = 0
			}
			if dir > 0 && p.next(next, 1) < 0 {
				p.offset = max(p.offset, len(p.rows)-m.bodyHeight())
			}
			return
		}
	}
	m.scroll(dir)
}

// scroll moves the view by n rows and keeps the selection on screen.
func (m *uiModel) scroll(n int) {
	p := m.page()
	h := m.bodyHeight()
	p.offset = min(max(0, p.offset+n), max(0, len(p.rows)-h))
	if p.cursor < 0 {
		return
	}
	if p.cursor < p.offset {
		if next := p.next(p.offset-1, 1); next >= 0 && next < p.offset+h {
			p.cursor = next
		}
	} else if p.cursor >= p.offset+h {
		if prev := p.next(p.offset+h, -1); prev >= p.offset {
			p.cursor = prev
		}
	}
}

// keepVisible scrolls so the selected row is on screen.
func (m *uiModel) keepVisible() {
	p := m.page()
	h := m.bodyHeight()
	if p.cursor >= 0 {
		if p.cursor < p.offset {
			p.offset = p.cursor
		}
		if p.cursor >= p.offset+h {
			p.offset = p.cursor - h + 1
		}
	}
	p.offset = min(p.offset, max(0, len(p.rows)-h))
}

// reload rebuilds the current page, keeping the selection where it was.
func (m *uiModel) reload() {
	old := m.page()
	if old.build == nil {
		return
	}
	page, err := old.build()
	if err != nil {
		m.status = err.Error()
		return
	}
	page.offset = old.offset
	if old.cursor >= 0 && old.cursor < len(page.rows) && page.rows[old.cursor].open != nil {
		page.cursor = old.cursor
	}
	m.stack[len(m.stack)-1] = page
}

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleTab      = lipgloss.NewStyle().Faint(true)
	styleTabOn    = lipgloss.NewStyle().Bold(true).Underline(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleHeading  = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Bold(true).Reverse(true)
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	// The accent: an assayer's gold.
	styleAccent  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleWelcome = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("214")).Padding(0, 1)
	styleStatus  = map[string]lipgloss.Style{
		"passed":   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		"accepted": lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		"failed":   lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		"rejected": lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		"error":    lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		"blocked":  lipgloss.NewStyle().Faint(true),
	}
)

// statusStyle colors text by a run, attempt, or result status.
func statusStyle(status, text string) string {
	if style, ok := styleStatus[status]; ok {
		return style.Render(text)
	}
	return text
}

func (m *uiModel) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "assay"
	return view
}

// render draws the screen as text, exactly width by height.
func (m *uiModel) render() string {
	w, h := m.width, m.bodyHeight()
	lines := make([]string, 0, m.height)
	top := 2 // the header and its rule
	if m.showWelcome() {
		welcome := m.welcomeLines()
		lines = append(lines, welcome...)
		top += len(welcome)
	}

	// Header: the tabs, then where we are.
	var tabs []string
	for i, name := range tabNames {
		if i == m.tab {
			tabs = append(tabs, styleTabOn.Render(name))
		} else {
			tabs = append(tabs, styleTab.Render(name))
		}
	}
	header := styleTitle.Render("assay") + "  " + strings.Join(tabs, "  ")
	var crumbs []string
	for _, page := range m.stack[1:] {
		crumbs = append(crumbs, page.title)
	}
	if len(crumbs) > 0 {
		header += styleDim.Render("  ›  ") + strings.Join(crumbs, styleDim.Render(" › "))
	}
	lines = append(lines, header, styleDim.Render(strings.Repeat("─", w)))

	if m.help {
		lines = append(lines, helpLines()...)
	} else {
		p := m.page()
		for i := p.offset; i < len(p.rows) && i < p.offset+h; i++ {
			row := p.rows[i]
			switch {
			case i == p.cursor:
				lines = append(lines, styleSelected.Render("› "+ansi.Strip(row.text)))
			default:
				lines = append(lines, "  "+row.text)
			}
		}
		if len(p.rows) == 0 {
			lines = append(lines, styleDim.Render("  nothing here yet"))
		}
	}
	for len(lines) < top+h {
		lines = append(lines, "")
	}
	lines = lines[:top+h]

	status := ""
	switch {
	case m.status != "":
		status = styleError.Render(oneLine(m.status))
	case m.busy != "":
		status = styleAccent.Render("◆ " + m.busy)
	case m.note != "":
		status = oneLine(m.note)
	}
	if p := m.page(); status == "" && !m.help && len(p.rows) > h {
		status = styleDim.Render(fmtRange(p.offset, min(len(p.rows), p.offset+h), len(p.rows)))
	}
	keys := m.keys()
	lines = append(lines, status, styleDim.Render(keys))
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, w, "…")
	}
	return strings.Join(lines, "\n")
}

// truncateLeft shortens s to width by cutting its start, which keeps the
// end of a path: the part that says which project it is.
func truncateLeft(s string, width int) string {
	if ansi.StringWidth(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && ansi.StringWidth(string(runes))+1 > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

func fmtRange(from, to, total int) string {
	return fmt.Sprintf("lines %d–%d of %d", from+1, to, total)
}

func helpLines() []string {
	section := func(title string, keys [][2]string) []string {
		lines := []string{styleHeading.Render("  " + title)}
		for _, k := range keys {
			lines = append(lines, "  "+padRight(k[0], 11)+" "+k[1])
		}
		return lines
	}
	lines := section("Move", [][2]string{
		{"↑ ↓  k j", "move between items"},
		{"enter → l", "open the selected item"},
		{"esc ← h", "go back"},
		{"pgup pgdn", "scroll a page; home end jump to the top or bottom"},
		{"tab, 1 2", "switch between Contracts and Runs"},
		{"ctrl+r", "reload from the database"},
		{"q", "go back, or quit from a list; ctrl+c quits from anywhere"},
	})
	lines = append(lines, "")
	return append(lines, section("Act on what's selected", [][2]string{
		{"v", "validate a contract file"},
		{"p", "publish a contract file, after showing what changes"},
		{"e", "edit a contract file in $EDITOR, then validate it"},
		{"c", "check the project against a published contract"},
		{"r", "recheck a run, adding an attempt"},
	})...)
}

func padRight(s string, width int) string {
	if n := ansi.StringWidth(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
