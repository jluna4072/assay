package cli

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"assay/backend"
	"assay/project"
)

// Work runs in the UI. A Work page follows a run through its stored
// events, the ones "assay show --follow" reads, so it shows runs started
// here and runs started by another process the same way.

// uiPoll is how often the UI reads new events and refreshes lists that
// show a running work run.
var uiPoll = 500 * time.Millisecond

// uiLive is the state of a Work page.
type uiLive struct {
	run      string
	after    int // the last event read
	log      bytes.Buffer
	printer  *workPrinter
	results  bool // showing the results instead of the log
	follow   bool // keep the log scrolled to its end
	finished bool
	current  backend.Run
}

// workPage follows a work run: its log as "assay show --follow" prints it,
// or its attempts and results.
func (d *uiData) workPage(id string) (*uiPage, error) {
	live := &uiLive{run: id, follow: true}
	live.printer = &workPrinter{w: &live.log, root: d.s.project.Root}
	page := &uiPage{title: "work " + shortID(id), cursor: -1, live: live}
	page.build = func() (*uiPage, error) { return page, d.poll(page) }
	page.target = uiTarget{run: id}
	if err := d.poll(page); err != nil {
		return nil, err
	}
	return page, nil
}

// poll reads a Work page's new events and redraws it.
func (d *uiData) poll(p *uiPage) error {
	live := p.live
	if !live.finished {
		events, err := d.s.service.Events(live.run, live.after)
		if err != nil {
			return err
		}
		run, err := d.s.service.GetRun(live.run)
		if err != nil {
			return err
		}
		if run.Work == nil {
			return fmt.Errorf("run %s wasn't made by \"assay work\"", shortID(run.ID))
		}
		for _, event := range events {
			live.after = event.Seq
			event.Run = &run
			live.printer.event(event)
			live.finished = live.finished || event.Type == backend.EventRunFinished
		}
		// A run recorded before events were stored has none to read.
		live.finished = live.finished || len(events) == 0 && run.Work.Outcome != backend.WorkRunning
		live.current = run
	}
	d.drawWork(p)
	return nil
}

func (d *uiData) drawWork(p *uiPage) {
	live, run := p.live, p.live.current
	w := run.Work
	attempts := len(run.Attempts)
	if !live.finished && w.Outcome == backend.WorkRunning {
		attempts++ // the one in progress
	}
	state := statusStyle(string(w.Outcome), strings.ReplaceAll(string(w.Outcome), "_", " "))
	if w.Outcome == backend.WorkRunning {
		state = styleAccent.Render("◆ running")
	}
	line := fmt.Sprintf("run %s · %s@%d · %s · %s · attempt %d of %d", shortID(run.ID), run.ContractID, run.Version, w.Worker, state, max(1, attempts), w.MaxAttempts)
	end := time.Now()
	if w.FinishedAt != nil {
		end = *w.FinishedAt
	}
	timing := fmt.Sprintf("started %s · %s of %s used", run.CreatedAt.Local().Format("15:04:05"), shortDuration(end.Sub(run.CreatedAt)), shortDuration(time.Duration(w.Timeout)*time.Millisecond))
	if w.Reason != "" {
		timing += " · " + w.Reason
	}
	if attempts > 1 && w.Outcome == backend.WorkRunning {
		timing += " · told the worker what failed"
	}
	tabs := []string{styleTabOn.Render("Log"), styleTab.Render("Results")}
	if live.results {
		tabs = []string{styleTab.Render("Log"), styleTabOn.Render("Results")}
	}
	rows := []uiRow{
		{text: line},
		{text: "task: " + oneLine(w.Task)},
		{text: styleDim.Render(timing)},
		blank,
		{text: strings.Join(tabs, "  ")},
	}
	header := len(rows)
	if live.results {
		rows = append(rows, d.attemptRows(run)...)
	} else {
		log := textRows(contentText(live.log.Bytes()))
		for len(log) > 0 && log[0].text == "" {
			log = log[1:]
		}
		rows = append(rows, log...)
		if !live.finished {
			rows = append(rows, uiRow{text: styleDim.Render("…")})
		}
	}
	cursor, offset := p.cursor, p.offset
	p.rows = rows
	p.cursor = -1
	if live.results {
		p.cursor = p.next(header-1, 1)
		if cursor > p.cursor && cursor < len(rows) && rows[cursor].open != nil {
			p.cursor = cursor
		}
		p.offset = offset
	}
}

// uiWork is a work run this UI started.
type uiWork struct {
	run      string // known once it starts
	cancel   context.CancelFunc
	started  chan string
	finished chan uiWorkFinishedMsg
}

type uiWorkStartedMsg struct{ run string }

type uiWorkFinishedMsg struct {
	run backend.Run
	err error
}

type uiTickMsg struct{}

// uiTask is the form that asks what work to do.
type uiTask struct {
	input    textinput.Model
	contract string
	version  int
	workers  []string
	worker   int
	page     *uiPage
}

// startWork asks for a task for a worker to do against a published
// version, as "assay work" takes one.
func (m *uiModel) startWork(t uiTarget) tea.Cmd {
	if m.work != nil {
		m.status = fmt.Sprintf("work run %s is still going; one at a time", shortID(m.work.run))
		return nil
	}
	id, version := t.contract, t.version
	if t.run != "" {
		run, err := m.data.s.service.GetRun(t.run)
		if err != nil {
			m.status = err.Error()
			return nil
		}
		id, version = run.ContractID, run.Version
	}
	if version == 0 {
		versions, err := m.data.s.service.ListVersions(id)
		if err != nil || len(versions) == 0 {
			m.status = fmt.Sprintf("%s has no published versions", id)
			return nil
		}
		version = versions[len(versions)-1].Version
	}
	config, err := project.LoadConfig(m.data.s.project.Root)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	def, err := config.Worker("")
	if err != nil && len(config.Workers) == 0 {
		m.status = err.Error() + " (see docs/cli.md, Workers)"
		return nil
	}
	var workers []string
	for name := range config.Workers {
		workers = append(workers, name)
	}
	slices.Sort(workers)
	chosen := 0
	if err == nil {
		chosen = slices.Index(workers, def.Name)
	}

	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "what the worker should do"
	input.SetWidth(max(20, m.width-8))
	ref := fmt.Sprintf("%s@%d", id, version)
	rows := []uiRow{
		{text: styleHeading.Render("Start work on " + ref)},
		{text: styleDim.Render(fmt.Sprintf("A worker does the task in a new git worktree. assay checks each attempt and"))},
		{text: styleDim.Render(fmt.Sprintf("sends back what failed, up to %d attempts in %s. Nothing is merged.", backend.DefaultMaxAttempts, shortDuration(backend.DefaultWorkTimeout)))},
		blank,
		{text: "Task"},
		{input: true},
		blank,
		{worker: true},
		blank,
		{text: styleDim.Render("For other limits or a task file, use: assay work " + ref + " --help")},
	}
	page := newPage("work on "+ref, rows, nil)
	m.welcome = nil
	m.stack = append(m.stack, page)
	m.task = &uiTask{input: input, contract: id, version: version, workers: workers, worker: chosen, page: page}
	return m.task.input.Focus()
}

// taskKey handles keys while the task form is open: enter starts the
// work, esc closes the form, tab picks the next worker, and everything
// else is typing.
func (m *uiModel) taskKey(msg tea.KeyPressMsg) tea.Cmd {
	t := m.task
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		m.closeTask()
		m.note = "no work started"
		return nil
	case "tab":
		if len(t.workers) > 1 {
			t.worker = (t.worker + 1) % len(t.workers)
		}
		return nil
	case "enter":
		task := strings.TrimSpace(t.input.Value())
		if task == "" {
			m.status = "say what the worker should do"
			return nil
		}
		m.closeTask()
		return m.runWork(backend.WorkOptions{
			ContractID: t.contract, Version: t.version, ProjectDir: m.data.s.project.Root,
			Task: task, Worker: t.workers[t.worker],
		})
	}
	m.status = ""
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	return cmd
}

func (m *uiModel) closeTask() {
	if n := len(m.stack); n > 1 && m.stack[n-1] == m.task.page {
		m.stack = m.stack[:n-1]
	}
	m.task = nil
}

// runWork starts "assay work" in the background. The first message says
// which run it made; the next says how it ended.
func (m *uiModel) runWork(options backend.WorkOptions) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	work := &uiWork{cancel: cancel, started: make(chan string, 1), finished: make(chan uiWorkFinishedMsg, 1)}
	m.work = work
	service := m.data.s.service
	go func() {
		run, err := service.Work(ctx, options, func(event backend.Event) {
			if event.Type == backend.EventRunStarted {
				select {
				case work.started <- event.Run.ID:
				default:
				}
			}
		})
		work.finished <- uiWorkFinishedMsg{run: run, err: err}
	}()
	m.busy = "starting " + options.Worker + "…"
	return func() tea.Msg {
		select {
		case id := <-work.started:
			return uiWorkStartedMsg{run: id}
		case done := <-work.finished:
			return done
		}
	}
}

func (m *uiModel) workStarted(msg uiWorkStartedMsg) tea.Cmd {
	m.busy = ""
	work := m.work
	work.run = msg.run
	if page, err := m.data.workPage(msg.run); err == nil {
		m.welcome = nil
		m.stack = append(m.stack, page)
	} else {
		m.status = err.Error()
	}
	return func() tea.Msg { return <-work.finished }
}

func (m *uiModel) workFinished(msg uiWorkFinishedMsg) tea.Cmd {
	m.busy = ""
	m.work = nil
	if msg.err != nil {
		m.status = "couldn't start work: " + msg.err.Error()
	} else if w := msg.run.Work; w != nil {
		outcome := string(w.Outcome)
		m.note = statusStyle(outcome, strings.ToUpper(strings.ReplaceAll(outcome, "_", " "))) + fmt.Sprintf(" · run %s · %s", shortID(msg.run.ID), w.Reason)
		if w.Outcome == backend.WorkAccepted {
			m.note += " · branch " + w.Branch
		}
	}
	m.pollAll()
	if m.quitting {
		return tea.Quit
	}
	return nil
}

// cancelWork asks, then stops a work run this UI started.
func (m *uiModel) cancelWork(t uiTarget) {
	switch {
	case t.run == "":
		m.status = "x cancels a work run: open one first"
	case m.work == nil || m.work.run != t.run:
		m.status = "x only cancels work started in this window; stop other runs where they were started"
	default:
		work := m.work
		m.confirm = &uiConfirm{yes: "cancel run " + shortID(work.run), no: "still working", do: func() (string, error) {
			work.cancel()
			m.busy = "cancelling…"
			return "", nil
		}}
	}
}

// quit leaves the UI, first asking to cancel work it started: the run
// would otherwise be left marked as running.
func (m *uiModel) quit() tea.Cmd {
	if m.work == nil {
		return tea.Quit
	}
	if m.quitting {
		return tea.Quit // asked twice: don't wait any longer
	}
	work := m.work
	m.confirm = &uiConfirm{yes: "cancel the work run and quit", no: "still working", quit: true, do: func() (string, error) {
		work.cancel()
		m.quitting = true
		m.busy = "cancelling the work run, then quitting…"
		return "", nil
	}}
	return nil
}

// tick schedules the next poll when something is live: a Work page that
// hasn't finished, a list with a running work run, or work started here.
func (m *uiModel) tick() tea.Cmd {
	if m.ticking {
		return nil
	}
	live := m.work != nil || m.page().active
	for _, p := range m.stack {
		live = live || p.live != nil && !p.live.finished
	}
	if !live {
		return nil
	}
	m.ticking = true
	return tea.Tick(uiPoll, func(time.Time) tea.Msg { return uiTickMsg{} })
}

func (m *uiModel) ticked() {
	m.ticking = false
	m.pollAll()
	if p := m.page(); p.active && p.live == nil && m.confirm == nil && m.task == nil {
		m.reload()
	}
}

// pollAll brings the Work pages up to date.
func (m *uiModel) pollAll() {
	for _, p := range m.stack {
		if p.live != nil {
			if err := m.data.poll(p); err != nil {
				m.status = err.Error()
			}
		}
	}
	m.followLog()
}

// followLog keeps a Work page's log scrolled to its end while it's
// followed.
func (m *uiModel) followLog() {
	p := m.page()
	if p.live != nil && !p.live.results && p.live.follow {
		p.offset = max(0, len(p.rows)-m.bodyHeight())
	}
}

func (m *uiModel) workerChoice() string {
	t := m.task
	if len(t.workers) == 0 {
		return styleDim.Render("worker: the default")
	}
	line := "worker: " + styleHeading.Render(t.workers[t.worker])
	if len(t.workers) > 1 {
		line += styleDim.Render(fmt.Sprintf("  (tab for the next of %d)", len(t.workers)))
	}
	return line
}

// shortDuration rounds to seconds and drops zero units: 30m, 1m5s, 12s.
func shortDuration(d time.Duration) string {
	text := d.Round(time.Second).String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}
