package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"assay/contractfile"
)

// The UI's actions. Each calls the backend function its command calls:
// validate and publish as "assay contract validate" and "publish" do,
// check and recheck as "assay check" and "recheck" do.

// uiConfirm is a yes-or-no question the current page asks.
type uiConfirm struct {
	yes  string  // what y does, for the key line, e.g. "publish docs@2"
	no   string  // what's said after n
	page *uiPage // the page the question is about, closed with it; may be nil
	quit bool    // the question is whether to quit
	do   func() (note string, err error)
}

// uiDoneMsg reports a check or recheck that ran in the background.
type uiDoneMsg struct {
	note string
	err  error
	run  string  // the run it added an attempt to
	from *uiPage // the page it was started from
}

// uiEditedMsg reports that the editor closed.
type uiEditedMsg struct {
	file string
	err  error
}

// target is what an action acts on: the selected row's subject, or the
// page's.
func (m *uiModel) target() uiTarget {
	p := m.page()
	if p.cursor >= 0 {
		if t := p.rows[p.cursor].target; t != (uiTarget{}) {
			return t
		}
	}
	return p.target
}

func (m *uiModel) act(key string) tea.Cmd {
	t := m.target()
	need := map[string]string{
		"v": "v validates a contract file: select one on the Contracts tab",
		"p": "p publishes a contract file: select one on the Contracts tab",
		"e": "e edits a contract file: select one on the Contracts tab",
		"c": "c checks the project against a published contract: select one",
		"r": "r rechecks a run: select one on the Runs tab",
	}
	switch {
	case (key == "v" || key == "p" || key == "e") && t.file == "",
		key == "c" && t.contract == "",
		key == "r" && t.run == "":
		m.status = need[key]
		return nil
	case m.busy != "" && (key == "c" || key == "r"):
		m.status = "wait for the check that's running to finish"
		return nil
	}
	switch key {
	case "v":
		m.validate(t.file)
	case "p":
		m.publish(t.file)
	case "e":
		return m.edit(t.file)
	case "c":
		return m.check(t.contract, t.version)
	case "r":
		return m.recheck(t.run)
	}
	return nil
}

func (m *uiModel) filePath(name string) string {
	return filepath.Join(m.data.s.project.ContractsPath(), name)
}

// validate checks a contract file as "assay contract validate" does and
// says what it found.
func (m *uiModel) validate(name string) {
	result, err := findConfig(m.data.s.project.Root).check(m.filePath(name))
	m.reload()
	switch {
	case err != nil:
		m.status = name + ": " + err.Error()
	case !result.Valid():
		m.status = fmt.Sprintf("✗ %s: %s — open it to see them", name, plural(len(result.Problems), "problem"))
	default:
		m.note = statusStyle("passed", "✓ ") + fmt.Sprintf("%s: %s, %s", name, result.Contract.ID, plural(len(result.Contract.Rules), "rule"))
	}
}

// publish shows what publishing a file would change and asks before
// publishing it, as "assay contract publish" asks for a terminal.
func (m *uiModel) publish(name string) {
	s := m.data.s
	result, err := findConfig(s.project.Root).check(m.filePath(name))
	switch {
	case err != nil:
		m.status = name + ": " + err.Error()
		return
	case !result.Valid():
		m.status = fmt.Sprintf("✗ %s: %s, so it can't be published — open it to see them", name, plural(len(result.Problems), "problem"))
		return
	}
	change, err := diffContract(s, result.Contract)
	if err != nil {
		m.status = err.Error()
		return
	}
	if !change.changed {
		m.note = fmt.Sprintf("%s@%d unchanged: %s matches the latest published version", change.latest.Contract.ID, change.latest.Version, name)
		return
	}
	next := 1
	if change.latest != nil {
		next = change.latest.Version + 1
	}
	ref := fmt.Sprintf("%s@%d", result.Contract.ID, next)
	before, after := diffHeader(change, filepath.Join(filepath.Base(s.project.ContractsPath()), name))
	rows := []uiRow{
		{text: styleHeading.Render(fmt.Sprintf("Publish %s as %s?", name, ref))},
		{text: styleDim.Render("Published versions never change; runs keep the version they started with.")},
		blank,
		{text: statusStyle("failed", "--- "+before)},
		{text: statusStyle("passed", "+++ "+after)},
	}
	for _, line := range trimDiff(change.lines) {
		switch line.op {
		case '+':
			rows = append(rows, uiRow{text: statusStyle("passed", line.String())})
		case '-':
			rows = append(rows, uiRow{text: statusStyle("failed", line.String())})
		case '.':
			rows = append(rows, uiRow{text: styleDim.Render("  …")})
		default:
			rows = append(rows, uiRow{text: line.String()})
		}
	}
	page := newPage("publish "+name, rows, nil).about(uiTarget{file: name})
	contract := change.contract // exactly what the diff shows
	m.welcome = nil
	m.stack = append(m.stack, page)
	m.confirm = &uiConfirm{yes: "publish " + ref, no: "nothing published", page: page, do: func() (string, error) {
		published, changed, err := s.service.PublishContent(contract)
		if err != nil {
			return "", err
		}
		at := fmt.Sprintf("%s@%d", published.Contract.ID, published.Version)
		if !changed {
			return at + " unchanged: it was published meanwhile", nil
		}
		return statusStyle("passed", "✓ ") + fmt.Sprintf("published %s (%s)", at, plural(len(published.Contract.Rules), "rule")), nil
	}}
}

// answer handles keys while a question is asked: y and n answer it, and
// the movement keys scroll what it's about.
func (m *uiModel) answer(key string) tea.Cmd {
	c := m.confirm
	switch key {
	case "y", "Y":
		m.closeConfirm()
		m.note, m.status = "", ""
		note, err := c.do()
		if err != nil {
			m.status = err.Error()
		} else {
			m.note = note
		}
		m.reload()
	case "n", "N", "esc", "q":
		m.closeConfirm()
		m.reload()
		m.note = c.no
	case "down", "j", "up", "k", "pgdown", "pgup", "space", "home", "end", "g", "G", "shift+g":
		m.confirm = nil
		m.key(key)
		m.confirm = c
	}
	return nil
}

func (m *uiModel) closeConfirm() {
	if n := len(m.stack); n > 1 && m.confirm.page != nil && m.stack[n-1] == m.confirm.page {
		m.stack = m.stack[:n-1]
	}
	m.confirm = nil
}

// edit opens a contract file in the person's editor; the UI checks it
// again when the editor closes.
func (m *uiModel) edit(name string) tea.Cmd {
	path := m.filePath(name)
	if contractfile.IsSuite(path) {
		path = filepath.Join(path, contractfile.SuiteFile) // its contracts are next to it
	}
	cmd := editorCommand(path)
	cmd.Dir = m.data.s.project.Root
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return uiEditedMsg{file: name, err: err} })
}

// editorCommand opens path in $VISUAL or $EDITOR, which may include
// arguments ("code --wait"), or vi.
func editorCommand(path string) *exec.Cmd {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		fields = []string{"vi"}
	}
	return exec.Command(fields[0], append(fields[1:], path)...)
}

func (m *uiModel) edited(msg uiEditedMsg) {
	m.status, m.note = "", ""
	if msg.err != nil {
		m.reload()
		m.status = "the editor failed: " + msg.err.Error()
		return
	}
	m.validate(msg.file)
}

// check starts a run of a published version, as "assay check" does, in
// the background.
func (m *uiModel) check(id string, version int) tea.Cmd {
	s := m.data.s
	if version == 0 {
		versions, err := s.service.ListVersions(id)
		if err != nil || len(versions) == 0 {
			m.status = fmt.Sprintf("%s has no published versions", id)
			return nil
		}
		version = versions[len(versions)-1].Version
	}
	m.busy = fmt.Sprintf("checking %s@%d…", id, version)
	from := m.page()
	return func() tea.Msg {
		run, err := s.service.StartRun(id, version, s.project.Root)
		if err != nil {
			return uiDoneMsg{err: err, from: from}
		}
		return evaluateMsg(s, run.ID, from)
	}
}

// recheck adds an attempt to a run, as "assay recheck" does.
func (m *uiModel) recheck(id string) tea.Cmd {
	s := m.data.s
	m.busy = fmt.Sprintf("rechecking run %s…", shortID(id))
	from := m.page()
	return func() tea.Msg { return evaluateMsg(s, id, from) }
}

func evaluateMsg(s *session, id string, from *uiPage) uiDoneMsg {
	run, err := s.service.EvaluateAttempt(id)
	if err != nil {
		return uiDoneMsg{err: err, run: id, from: from}
	}
	attempt := run.Attempts[len(run.Attempts)-1]
	status := string(attempt.Status)
	note := statusStyle(status, statusMark(attempt.Status)+" "+strings.ToUpper(status)) +
		fmt.Sprintf(" · %s@%d · run %s · attempt %d · %s", run.ContractID, run.Version, shortID(run.ID), attempt.Number, attemptCounts(attempt))
	return uiDoneMsg{note: note, run: run.ID, from: from}
}

// done shows a finished check: the run it made, if the person is still
// where they started it, or else just the outcome.
func (m *uiModel) done(msg uiDoneMsg) {
	m.busy = ""
	m.status, m.note = "", ""
	if msg.err != nil {
		m.reload()
		m.status = msg.err.Error()
		return
	}
	m.note = msg.note
	if p := m.page(); p == msg.from && p.target.run != msg.run && m.confirm == nil {
		if page, err := m.data.runPage(msg.run); err == nil {
			m.welcome = nil
			m.stack = append(m.stack, page)
			return
		}
	}
	m.reload()
}

// keys is the key line: the actions for what's selected, then moving
// around.
func (m *uiModel) keys() string {
	switch {
	case m.help:
		return "esc close help · ctrl+c quit"
	case m.confirm != nil:
		if m.confirm.page == nil {
			return "y " + m.confirm.yes + " · n " + m.confirm.no
		}
		return "y " + m.confirm.yes + " · n cancel · ↑↓ scroll"
	case m.task != nil:
		if len(m.task.workers) > 1 {
			return "enter start · tab next worker · esc cancel"
		}
		return "enter start · esc cancel"
	}
	var parts []string
	p := m.page()
	switch t := m.target(); {
	case t.file != "":
		parts = append(parts, "e edit", "p publish", "v validate")
	case t.contract != "":
		parts = append(parts, "c check", "w work")
	case t.run != "":
		if m.work != nil && m.work.run == t.run {
			parts = append(parts, "x cancel")
		}
		parts = append(parts, "r recheck", "w work again")
	}
	if p.live != nil {
		if p.live.results {
			parts = append(parts, "tab log")
		} else {
			parts = append(parts, "tab results")
		}
	}
	if p.cursor >= 0 {
		parts = append(parts, "enter open")
	}
	if len(m.stack) > 1 {
		parts = append(parts, "esc back")
	} else if p.live == nil {
		parts = append(parts, "tab "+strings.ToLower(tabNames[(m.tab+1)%len(tabNames)]))
	}
	parts = append(parts, "? help", "q quit")
	return strings.Join(parts, " · ")
}
