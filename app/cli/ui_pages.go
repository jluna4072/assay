package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"assay/backend"
	"github.com/charmbracelet/x/ansi"
)

// uiData builds the UI's pages from an open project.
type uiData struct {
	s *session
}

func textRows(text string) []uiRow {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	var rows []uiRow
	for _, line := range strings.Split(text, "\n") {
		rows = append(rows, uiRow{text: line})
	}
	return rows
}

func heading(text string) uiRow { return uiRow{text: styleHeading.Render(text)} }

var blank = uiRow{}

// contractsPage lists the contract files in assay/ with whether each is
// valid and published, then the published contracts.
func (d *uiData) contractsPage() (*uiPage, error) {
	dir := d.s.project.ContractsPath()
	files, err := contractFiles(dir)
	if err != nil {
		return nil, err
	}
	published, err := d.s.service.ListContracts()
	if err != nil {
		return nil, err
	}
	latest := map[string]backend.PublishedContract{}
	counts := map[string]int{}
	for _, c := range published {
		versions, err := d.s.service.ListVersions(c.ID)
		if err != nil {
			return nil, err
		}
		if n := len(versions); n > 0 {
			latest[c.ID], counts[c.ID] = versions[n-1], n
		}
	}

	rows := []uiRow{heading(fmt.Sprintf("Contract files in %s/", filepath.Base(dir)))}
	if len(files) == 0 {
		rows = append(rows, uiRow{text: styleDim.Render("none yet — add a YAML file to " + filepath.Base(dir) + "/")})
	}
	config := findConfig(d.s.project.Root)
	width := 0
	for _, name := range files {
		width = max(width, len(name))
	}
	for _, name := range files {
		path := filepath.Join(dir, name)
		result, err := config.check(path)
		var line string
		switch {
		case err != nil:
			line = statusStyle("error", "! "+padRight(name, width)) + "  " + oneLine(err.Error())
		case !result.Valid():
			line = statusStyle("failed", "✗ "+padRight(name, width)) + "  " + plural(len(result.Problems), "problem")
		default:
			c := result.Contract
			line = statusStyle("passed", "✓ ") + padRight(name, width) + "  " + c.ID + " · " + plural(len(c.Rules), "rule") + " · "
			if p, ok := latest[c.ID]; !ok {
				line += "not published"
			} else if backend.SameContent(p.Contract, c) {
				line += fmt.Sprintf("published as v%d", p.Version)
			} else {
				line += fmt.Sprintf("changed since v%d", p.Version)
			}
		}
		rows = append(rows, uiRow{text: line, target: uiTarget{file: name}, open: func() (*uiPage, error) { return d.filePage(name) }})
	}

	rows = append(rows, blank, heading("Published contracts"))
	if len(published) == 0 {
		rows = append(rows, uiRow{text: styleDim.Render("none yet — publish one with: assay contract publish <file>")})
	}
	width = 0
	for _, c := range published {
		width = max(width, len(c.ID))
	}
	for _, c := range published {
		p := latest[c.ID]
		line := fmt.Sprintf("%s  latest v%d · %s · %s", padRight(c.ID, width), p.Version, plural(counts[c.ID], "version"), c.Name)
		rows = append(rows, uiRow{text: line, target: uiTarget{contract: c.ID}, open: func() (*uiPage, error) { return d.contractPage(c.ID) }})
	}
	return newPage("Contracts", rows, d.contractsPage), nil
}

// filePage shows a contract file's problems, or its rules.
func (d *uiData) filePage(name string) (*uiPage, error) {
	path := filepath.Join(d.s.project.ContractsPath(), name)
	result, err := findConfig(d.s.project.Root).check(path)
	if err != nil {
		return nil, err
	}
	rel := filepath.Join(filepath.Base(d.s.project.ContractsPath()), name)
	var rows []uiRow
	if !result.Valid() {
		rows = append(rows, uiRow{text: statusStyle("failed", "✗ "+rel+": "+plural(len(result.Problems), "problem"))}, blank)
		for _, problem := range result.Problems {
			rows = append(rows, uiRow{text: rel + ":" + problem.String()})
		}
		rows = append(rows, blank, uiRow{text: styleDim.Render("Press e to edit the file; it's checked again when the editor closes.")})
		return newPage(name, rows, func() (*uiPage, error) { return d.filePage(name) }).about(uiTarget{file: name}), nil
	}
	c := result.Contract
	rows = append(rows, uiRow{text: statusStyle("passed", "✓ ") + rel + ": " + c.ID + " · " + c.Name}, blank)
	rows = append(rows, ruleRows(c, "Rules")...)
	return newPage(name, rows, func() (*uiPage, error) { return d.filePage(name) }).about(uiTarget{file: name}), nil
}

// ruleRows draws a contract's rules as the dependency tree from
// "assay contract graph", then each rule's check.
func ruleRows(c backend.Contract, title string) []uiRow {
	var tree bytes.Buffer
	writeTree(&tree, newRuleGraph(c))
	rows := []uiRow{heading(title)}
	rows = append(rows, textRows(tree.String())...)
	rows = append(rows, blank, heading("Checks"))
	width := 0
	for _, rule := range c.Rules {
		width = max(width, len(rule.ID))
	}
	for _, rule := range c.Rules {
		check := string(rule.Kind)
		switch rule.Kind {
		case backend.FileExists:
			check += " " + rule.Path
		case backend.CommandSucceeds:
			check += " " + rule.Command
		case backend.ReviewCheck:
			if rule.Reviewer != "" {
				check += " by " + rule.Reviewer
			}
		}
		if n := len(rule.Evidence); n > 0 {
			check += " · " + plural(n, "evidence item")
		}
		rows = append(rows, uiRow{text: padRight(rule.ID, width) + "  " + check})
	}
	return rows
}

// contractPage shows a published contract: its versions, the latest
// version's rules, and its runs.
func (d *uiData) contractPage(id string) (*uiPage, error) {
	versions, err := d.s.service.ListVersions(id)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("%s has no published versions", id)
	}
	latest := versions[len(versions)-1]
	rows := []uiRow{{text: fmt.Sprintf("%s · %s", latest.Contract.ID, latest.Contract.Name)}, blank, heading("Versions")}
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		line := fmt.Sprintf("v%-3d published %s · %s", v.Version, v.PublishedAt.Local().Format("2006-01-02 15:04"), plural(len(v.Contract.Rules), "rule"))
		rows = append(rows, uiRow{text: line, target: uiTarget{contract: id, version: v.Version}, open: func() (*uiPage, error) { return d.versionPage(id, v.Version) }})
	}
	rows = append(rows, blank)
	runs, err := listRuns(d.s)
	if err != nil {
		return nil, err
	}
	var mine []backend.Run
	for _, run := range runs {
		if run.ContractID == id {
			mine = append(mine, run)
		}
	}
	rows = append(rows, heading("Runs"))
	if len(mine) == 0 {
		rows = append(rows, uiRow{text: styleDim.Render("none yet — start one with: assay check " + id)})
	}
	runRows, active, err := d.runRows(mine)
	if err != nil {
		return nil, err
	}
	rows = append(rows, runRows...)
	rows = append(rows, blank)
	rows = append(rows, ruleRows(latest.Contract, fmt.Sprintf("Rules in v%d", latest.Version))...)
	page := newPage(id, rows, func() (*uiPage, error) { return d.contractPage(id) }).about(uiTarget{contract: id})
	page.active = active
	return page, nil
}

func (d *uiData) versionPage(id string, version int) (*uiPage, error) {
	v, err := d.s.service.GetVersion(id, version)
	if err != nil {
		return nil, err
	}
	rows := []uiRow{
		{text: fmt.Sprintf("%s@%d · %s", id, version, v.Contract.Name)},
		{text: styleDim.Render("published " + v.PublishedAt.Local().Format("2006-01-02 15:04"))},
		blank,
	}
	rows = append(rows, ruleRows(v.Contract, "Rules")...)
	return newPage(fmt.Sprintf("v%d", version), rows, func() (*uiPage, error) { return d.versionPage(id, version) }).about(uiTarget{contract: id, version: version}), nil
}

// runsPage lists every run, newest first, as "assay runs" does.
func (d *uiData) runsPage() (*uiPage, error) {
	runs, err := listRuns(d.s)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		rows := []uiRow{{text: styleDim.Render(`no runs yet. Start one with "assay check <contract>".`)}}
		return newPage("Runs", rows, d.runsPage), nil
	}
	rows, active, err := d.runRows(runs)
	if err != nil {
		return nil, err
	}
	page := newPage("Runs", rows, d.runsPage)
	page.active = active
	return page, nil
}

// runRows is a table of runs with a header, each row opening its run.
// A work run's rows open its Work page; active reports that one is still
// running, so the list should keep itself up to date.
func (d *uiData) runRows(runs []backend.Run) (rows []uiRow, active bool, err error) {
	if len(runs) == 0 {
		return nil, false, nil
	}
	full := make([]backend.Run, len(runs))
	var table bytes.Buffer
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tCONTRACT\tSTATUS\tATTEMPTS\tSTARTED\tWORK")
	for i, run := range runs {
		var err error
		if full[i], err = d.s.service.GetRun(run.ID); err != nil {
			return nil, false, err
		}
		work := ""
		if w := full[i].Work; w != nil {
			work = w.Worker + " · " + strings.ReplaceAll(string(w.Outcome), "_", " ")
			active = active || w.Outcome == backend.WorkRunning
		}
		// Statuses are padded here and colored after, so color codes
		// don't upset the column widths.
		fmt.Fprintf(tw, "%s\t%s@%d\t%s\t%d\t%s\t%s\n", shortID(run.ID), run.ContractID, run.Version, full[i].Status, len(full[i].Attempts), run.CreatedAt.Local().Format("2006-01-02 15:04"), work)
	}
	tw.Flush()
	lines := strings.Split(strings.TrimRight(table.String(), "\n"), "\n")
	rows = []uiRow{{text: styleDim.Render(lines[0])}}
	for i, line := range lines[1:] {
		run := full[i]
		if status := run.Status; status != "" {
			if at := strings.Index(line, " "+status+" "); at >= 0 {
				line = line[:at+1] + statusStyle(status, status) + line[at+1+len(status):]
			}
		}
		open := func() (*uiPage, error) { return d.runPage(run.ID) }
		if run.Work != nil {
			open = func() (*uiPage, error) { return d.workPage(run.ID) }
		}
		rows = append(rows, uiRow{text: line, target: uiTarget{run: run.ID}, open: open})
	}
	return rows, active, nil
}

// runPage shows a run as "assay show" does: every attempt, its worker,
// and each rule's result, which opens the rule's evidence.
func (d *uiData) runPage(id string) (*uiPage, error) {
	run, err := d.s.service.GetRun(id)
	if err != nil {
		return nil, err
	}
	started := "started " + run.CreatedAt.Local().Format("2006-01-02 15:04")
	if run.Baseline != "" {
		started += " at " + shortCommit(run.Baseline)
	}
	title := fmt.Sprintf("run %s · %s@%d", shortID(run.ID), run.ContractID, run.Version)
	if run.ProjectDir != d.s.project.Root {
		title += " · " + run.ProjectDir
	}
	rows := []uiRow{
		{text: title},
		{text: started + " · " + statusStyle(run.Status, runStatusLine(run))},
	}
	if w := run.Work; w != nil {
		outcome := strings.ReplaceAll(string(w.Outcome), "_", " ")
		line := "work by " + w.Worker + " · " + statusStyle(string(w.Outcome), outcome)
		if w.Reason != "" {
			line += ": " + w.Reason
		}
		rows = append(rows,
			uiRow{text: line},
			uiRow{text: fmt.Sprintf("branch %s · workspace %s", w.Branch, relative(run.ProjectDir, w.Workspace))},
			uiRow{text: "task: " + oneLine(w.Task)})
	}
	rows = append(rows, d.attemptRows(run)...)
	return newPage("run "+shortID(run.ID), rows, func() (*uiPage, error) { return d.runPage(id) }).about(uiTarget{run: id}), nil
}

// attemptRows lists a run's attempts, newest first since it decides the
// run, each with its worker and its rules' results, which open.
func (d *uiData) attemptRows(run backend.Run) []uiRow {
	var rows []uiRow
	for i := len(run.Attempts) - 1; i >= 0; i-- {
		attempt := run.Attempts[i]
		rows = append(rows, blank, heading(fmt.Sprintf("attempt %d · %s · %s", attempt.Number, attempt.Status, attempt.StartedAt.Local().Format("2006-01-02 15:04:05"))))
		if attempt.Worker != nil {
			worker := *attempt.Worker
			rows = append(rows, uiRow{text: "⚙ " + describeWorker(worker), open: func() (*uiPage, error) {
				return d.workerPage(run, attempt.Number, worker)
			}})
		}
		results, width := sortedResults(attempt)
		for _, r := range results {
			text := statusStyle(string(r.Status), statusMark(r.Status)) + " " + resultLine(r, width)
			rows = append(rows, uiRow{text: text, open: func() (*uiPage, error) { return d.resultPage(run, attempt.Number, r) }})
		}
	}
	if len(run.Attempts) == 0 {
		rows = append(rows, blank, uiRow{text: styleDim.Render("no attempts yet")})
	}
	return rows
}

// resultPage shows a rule's result in one attempt with all its evidence.
func (d *uiData) resultPage(run backend.Run, attempt int, r backend.RuleResult) (*uiPage, error) {
	rows := []uiRow{{text: statusStyle(string(r.Status), statusMark(r.Status)+" "+r.RuleID+" · "+string(r.Status)) + styleDim.Render(fmt.Sprintf(" · attempt %d", attempt))}}
	if reason := strings.TrimSpace(r.Reason); reason != "" {
		rows = append(rows, blank)
		rows = append(rows, textRows(contentText([]byte(reason)))...)
	}
	if r.Review != nil {
		rows = append(rows, blank, uiRow{text: "review by " + r.Review.Reviewer + " · " + describeReview(*r.Review)})
	}
	if len(r.Evidence) == 0 {
		why := "its contract asks for none"
		if r.Status == backend.Blocked {
			why = "it was blocked, so it wasn't checked"
		}
		rows = append(rows, blank, uiRow{text: styleDim.Render("no evidence: " + why)})
	}
	for i, item := range r.Evidence {
		rows = append(rows, blank, heading(fmt.Sprintf("[%d] %s", i+1, describeEvidence(item))))
		rows = append(rows, d.blobRows(item)...)
	}
	title := fmt.Sprintf("%s · attempt %d", r.RuleID, attempt)
	return newPage(title, rows, nil).about(uiTarget{run: run.ID}), nil
}

// workerPage shows what a worker was given and what it printed.
func (d *uiData) workerPage(run backend.Run, attempt int, w backend.WorkerRun) (*uiPage, error) {
	rows := []uiRow{{text: describeWorker(w)}, {text: styleDim.Render(strings.Join(w.Argv, " "))}}
	rows = append(rows, blank, heading("Prompt · "+formatSize(w.Prompt.Size)))
	rows = append(rows, d.blobRows(w.Prompt)...)
	rows = append(rows, blank, heading("Output · "+formatSize(w.Output.Size)))
	rows = append(rows, d.blobRows(w.Output)...)
	return newPage(fmt.Sprintf("worker · attempt %d", attempt), rows, nil).about(uiTarget{run: run.ID}), nil
}

// blobRows is an evidence item's stored content, one row per line.
func (d *uiData) blobRows(item backend.Evidence) []uiRow {
	switch {
	case item.Error != "":
		return []uiRow{{text: styleError.Render("couldn't collect: " + item.Error)}}
	case item.Absent:
		return []uiRow{{text: styleDim.Render("absent")}}
	case item.Blob == "":
		return []uiRow{{text: styleDim.Render("nothing stored")}}
	}
	content, err := d.s.service.GetEvidence(item.Blob)
	if errors.Is(err, backend.ErrNotFound) || (err == nil && content == nil) {
		return []uiRow{{text: styleDim.Render("nothing stored")}}
	}
	if err != nil {
		return []uiRow{{text: styleError.Render("couldn't read it: " + err.Error())}}
	}
	if len(content) == 0 {
		return []uiRow{{text: styleDim.Render("(empty)")}}
	}
	if !utf8.Valid(content) {
		return []uiRow{{text: styleDim.Render(fmt.Sprintf("binary content, %s — print it with assay evidence", formatSize(int64(len(content)))))}}
	}
	// Review requests and responses are one long line of JSON; indent them
	// so they can be read without scrolling sideways.
	if item.Kind == backend.ReviewRequestEvidence || item.Kind == backend.ReviewResponseEvidence {
		var indented bytes.Buffer
		if json.Indent(&indented, content, "", "  ") == nil {
			content = indented.Bytes()
		}
	}
	return textRows(contentText(content))
}

// contentText makes stored text safe to draw: no terminal escapes or
// carriage returns, and tabs expanded.
func contentText(content []byte) string {
	text := ansi.Strip(string(content))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		col := 0
		for _, r := range line {
			switch {
			case r == '\t':
				n := 4 - col%4
				b.WriteString(strings.Repeat(" ", n))
				col += n
			case r < ' ' || r == 0x7f:
				b.WriteRune('�')
				col++
			default:
				b.WriteRune(r)
				col++
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// welcome summarizes the project for the welcome panel.
func (d *uiData) welcome() *uiWelcome {
	w := &uiWelcome{version: version(), project: d.s.project.Root}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, w.project); err == nil && !strings.HasPrefix(rel, "..") {
			w.project = filepath.Join("~", rel)
		}
	}
	files, _ := contractFiles(d.s.project.ContractsPath())
	published, _ := d.s.service.ListContracts()
	runs, _ := listRuns(d.s)
	parts := []string{plural(len(files), "contract file"), fmt.Sprintf("%d published", len(published)), plural(len(runs), "run")}
	if len(runs) > 0 {
		if last, err := d.s.service.GetRun(runs[0].ID); err == nil {
			status := last.Status
			if status == "" {
				status = "no attempts yet"
			}
			parts = append(parts, "last run: "+statusStyle(last.Status, status))
		}
	}
	w.summary = strings.Join(parts, " · ")
	return w
}
