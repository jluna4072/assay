package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// uiProject is evidenceProject with one failed and one passed attempt,
// plus an invalid contract file and an unpublished one.
func uiProject(t *testing.T) string {
	t.Helper()
	dir := evidenceProject(t)
	os.WriteFile(filepath.Join(dir, "assay", "broken.yaml"), []byte("id: broken\nname: Broken\nrules:\n  - id: a\n    check: file_exists\n"), 0644)
	runIn(dir, false, "check", "docs")
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# Guide\n\tIndented with a tab.\n"), 0644)
	mustRun(t, dir, "recheck")
	return dir
}

func newTestUI(t *testing.T, dir string, tab int) *uiModel {
	t.Helper()
	var stderr bytes.Buffer
	s, code := openProject(Env{Stdout: io.Discard, Stderr: &stderr, Dir: dir}, &uiCommand, "")
	if s == nil {
		t.Fatalf("open project: exit %d: %s", code, stderr.String())
	}
	t.Cleanup(s.Close)
	m := newUIModel(&uiData{s: s}, tab)
	m.welcome = m.data.welcome()
	m.welcome.version = "vTEST"
	if !strings.Contains(m.welcome.project, filepath.Base(dir)) {
		t.Fatalf("welcome names project %q", m.welcome.project)
	}
	m.welcome.project = "~/code/a-project-with-a-rather-long-name-that-does-not-fit-the-box"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

var keyCodes = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
	"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd,
}

// press sends keys by name and returns the command the last one gave.
func press(m *uiModel, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		msg := tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		if code, ok := keyCodes[k]; ok {
			msg = tea.KeyPressMsg{Code: code}
		} else if letter, ok := strings.CutPrefix(k, "ctrl+"); ok {
			msg = tea.KeyPressMsg{Code: rune(letter[0]), Mod: tea.ModCtrl}
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

var screenTimePattern = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d(:\d\d)?`)

// screen is what the UI draws, without styling, with values that change
// on every run replaced.
func screen(t *testing.T, m *uiModel, dir string) string {
	t.Helper()
	text := ansi.Strip(m.render())
	lines := strings.Split(text, "\n")
	if len(lines) != m.height {
		t.Errorf("screen has %d lines, want %d:\n%s", len(lines), m.height, text)
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > m.width {
			t.Errorf("line is %d wide, more than %d: %q", w, m.width, line)
		}
	}
	text = strings.ReplaceAll(text, dir, "PROJECT")
	text = screenTimePattern.ReplaceAllString(text, "TIME")
	for _, id := range runIDs(m) {
		text = regexp.MustCompile(`\b`+shortID(id)+`\b`).ReplaceAllString(text, "SHORT")
	}
	text = regexp.MustCompile(`sha256 [0-9a-f]{12}`).ReplaceAllString(text, "sha256 HASH")
	text = regexp.MustCompile(`at [0-9a-f]{7}\b`).ReplaceAllString(text, "at COMMIT")
	text = regexp.MustCompile(`since [0-9a-f]{7}\b`).ReplaceAllString(text, "since COMMIT")
	text = regexp.MustCompile(`in \d+(\.\d+)?m?s`).ReplaceAllString(text, "in DURATION")
	return text
}

func runIDs(m *uiModel) []string {
	runs, _ := m.data.s.service.ListRuns()
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	return ids
}

func TestUIBrowse(t *testing.T) {
	dir := uiProject(t)
	m := newTestUI(t, dir, tabContracts)
	golden(t, "ui-contracts.txt", dir, screen(t, m, dir))

	// The first selectable row is the first contract file.
	press(m, "enter")
	if got := m.page().title; got != "broken.yaml" {
		t.Fatalf("opened %q, want broken.yaml", got)
	}
	golden(t, "ui-file-invalid.txt", dir, screen(t, m, dir))
	press(m, "esc")
	if m.welcome != nil {
		t.Fatal("the welcome panel is still there after opening a file")
	}
	golden(t, "ui-contracts-after-welcome.txt", dir, screen(t, m, dir))
	press(m, "end", "enter")
	if got := m.page().title; got != "docs" {
		t.Fatalf("opened %q, want docs", got)
	}
	golden(t, "ui-contract.txt", dir, screen(t, m, dir))
	press(m, "esc")

	press(m, "tab")
	if m.tab != tabRuns || len(m.stack) != 1 {
		t.Fatalf("tab: tab %d, %d pages", m.tab, len(m.stack))
	}
	golden(t, "ui-runs.txt", dir, screen(t, m, dir))
	press(m, "enter")
	golden(t, "ui-run.txt", dir, screen(t, m, dir))

	// The newest attempt comes first; its last result is guide, which
	// opens its evidence.
	press(m, "down", "enter")
	if got := m.page().title; got != "guide · attempt 2" {
		t.Fatalf("opened %q", got)
	}
	golden(t, "ui-result.txt", dir, screen(t, m, dir))

	press(m, "?")
	golden(t, "ui-help.txt", dir, screen(t, m, dir))
	press(m, "esc")
	if m.help || m.page().title != "guide · attempt 2" {
		t.Fatal("esc didn't close help")
	}

	// Back out to the list, and quit from there.
	press(m, "esc", "esc")
	if len(m.stack) != 1 {
		t.Fatalf("%d pages after going back", len(m.stack))
	}
	if cmd := press(m, "q"); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("q on a list didn't quit")
	}
	if cmd := press(m, "enter", "ctrl+c"); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("ctrl+c didn't quit")
	}
}

func TestUIWelcomeFitsSmallTerminals(t *testing.T) {
	dir := uiProject(t)
	m := newTestUI(t, dir, tabContracts)
	for _, size := range []struct {
		width, height    int
		welcome, bigLogo bool
	}{
		{80, 24, true, true},
		{40, 24, true, false},
		{80, bigLogoMinHeight - 1, true, false},
		{80, welcomeMinHeight - 1, false, false},
	} {
		m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		text := screen(t, m, dir) // checks every line fits
		welcome, big := strings.Contains(text, "Welcome to assay"), strings.Contains(text, logo[0])
		if welcome != size.welcome || big != size.bigLogo {
			t.Errorf("%dx%d: welcome %v, logo %v:\n%s", size.width, size.height, welcome, big, text)
		}
		if m.bodyHeight() < 4 {
			t.Errorf("%dx%d: only %d rows for the list", size.width, size.height, m.bodyHeight())
		}
	}
}

func TestUIScrollsAndReloads(t *testing.T) {
	dir := uiProject(t)
	m := newTestUI(t, dir, tabRuns)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	press(m, "enter", "end")
	p := m.page()
	if p.offset == 0 || p.offset+m.bodyHeight() != len(p.rows) {
		t.Fatalf("end: offset %d of %d rows", p.offset, len(p.rows))
	}
	if p.cursor < p.offset {
		t.Fatalf("cursor %d is above the view at %d", p.cursor, p.offset)
	}
	screen(t, m, dir) // checks the size
	press(m, "home")
	if p.offset != 0 {
		t.Fatalf("home: offset %d", p.offset)
	}

	// A new attempt shows up after r.
	before := len(m.page().rows)
	mustRun(t, dir, "recheck")
	press(m, "ctrl+r")
	if len(m.page().rows) <= before {
		t.Fatalf("reload: %d rows, had %d", len(m.page().rows), before)
	}
	if !strings.Contains(ansi.Strip(m.render()), "attempt 3") {
		t.Fatalf("reload doesn't show attempt 3:\n%s", ansi.Strip(m.render()))
	}
}

func TestUINeedsATerminal(t *testing.T) {
	dir := newProject(t)
	_, stderr, code := runIn(dir, false, "ui")
	if code != ExitUsage || !strings.Contains(stderr, "needs a terminal") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// Without a terminal, plain "assay" still prints the overview.
	_, stderr, code = runIn(dir, false)
	if code != ExitUsage || !strings.Contains(stderr, "Usage:") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestUIOffersInitOutsideAProject(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	answers := []string{"n\n"}
	e := Env{Stdout: &out, Stderr: &errOut, Dir: dir, Terminal: true, ReadLine: func() (string, error) {
		answer := answers[0]
		return answer, nil
	}}
	if code := Run([]string{"ui"}, e); code != ExitUsage || !strings.Contains(errOut.String(), "Nothing created") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".assay")); err == nil {
		t.Fatal("declining created a project")
	}
}

// TestUIInATerminal runs the real binary in a pseudo-terminal: it opens
// the UI, switches tabs, quits, and leaves the terminal as it found it.
func TestUIInATerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := uiProject(t)
	bin := filepath.Join(t.TempDir(), "assay")
	if out, err := exec.Command("go", "build", "-o", bin, "assay/cmd/assay").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	editor := filepath.Join(t.TempDir(), "editor")
	os.WriteFile(editor, []byte("#!/bin/sh\nsed -i.bak 's/Project has a guide/Project has a user guide/' \"$1\"\n"), 0755)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=1", "VISUAL=", "EDITOR="+editor)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()

	var output safeBuffer
	go io.Copy(&output, terminal)
	waitFor := func(text string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(ansi.Strip(output.String()), text) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q:\n%q", text, output.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("Contract files in assay/")
	// Edit docs.yaml (the second file) with an "editor" that changes it,
	// then publish the change.
	terminal.Write([]byte("je"))
	waitFor("✓ docs.yaml: docs, 2 rules")
	terminal.Write([]byte("p"))
	waitFor("Publish docs.yaml as docs@2?")
	terminal.Write([]byte("y"))
	waitFor("published docs@2 (2 rules)")
	terminal.Write([]byte("2"))
	waitFor("ATTEMPTS")
	terminal.Write([]byte("q"))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("assay exited with %v:\n%q", err, output.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("assay didn't quit:\n%q", output.String())
	}
	if out := mustRun(t, dir, "contract", "versions", "docs"); !strings.Contains(out, "2") {
		t.Errorf("docs@2 wasn't published:\n%s", out)
	}
	raw := output.String()
	if !strings.Contains(raw, "\x1b[?1049h") || !strings.Contains(raw, "\x1b[?1049l") {
		t.Errorf("didn't enter and leave the alternate screen:\n%q", raw)
	}
	if regexp.MustCompile(`\x1b\[[0-9;]*(3[0-7]|38;[0-9;]+)m`).MatchString(raw) {
		t.Errorf("drew colors with NO_COLOR set:\n%q", raw)
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestUIValidateAndPublish(t *testing.T) {
	dir := uiProject(t)
	m := newTestUI(t, dir, tabContracts)
	press(m, "v")
	if !strings.Contains(m.status, "broken.yaml: 2 problems") {
		t.Fatalf("validate broken.yaml: %q", m.status)
	}
	press(m, "p")
	if !strings.Contains(m.status, "can't be published") || m.confirm != nil {
		t.Fatalf("publish broken.yaml: %q", m.status)
	}
	press(m, "down", "v")
	if !strings.Contains(ansi.Strip(m.note), "✓ docs.yaml: docs, 2 rules") {
		t.Fatalf("validate docs.yaml: %q %q", m.note, m.status)
	}
	press(m, "p")
	if m.confirm != nil || !strings.Contains(m.note, "docs@1 unchanged") {
		t.Fatalf("publish unchanged: %q", m.note)
	}

	os.WriteFile(filepath.Join(dir, "assay", "docs.yaml"), []byte(strings.Replace(evidenceContract, "Project has a guide", "Project has a user guide", 1)), 0644)
	press(m, "p")
	if m.confirm == nil {
		t.Fatalf("no confirmation: %q %q", m.status, m.note)
	}
	golden(t, "ui-publish.txt", dir, screen(t, m, dir))
	press(m, "n")
	if m.confirm != nil || len(m.stack) != 1 || m.note != "nothing published" {
		t.Fatalf("cancel: %d pages, %q", len(m.stack), m.note)
	}
	if versions, _ := m.data.s.service.ListVersions("docs"); len(versions) != 1 {
		t.Fatalf("cancelling published: %d versions", len(versions))
	}
	press(m, "p", "j", "y")
	if versions, _ := m.data.s.service.ListVersions("docs"); len(versions) != 2 {
		t.Fatalf("y didn't publish: %d versions", len(versions))
	}
	if !strings.Contains(ansi.Strip(m.note), "published docs@2 (2 rules)") || m.confirm != nil || len(m.stack) != 1 {
		t.Fatalf("after publishing: %q, %d pages", m.note, len(m.stack))
	}
	if !strings.Contains(ansi.Strip(m.render()), "published as v2") {
		t.Fatalf("the list wasn't reloaded:\n%s", ansi.Strip(m.render()))
	}
}

func TestUIEdit(t *testing.T) {
	dir := uiProject(t)
	m := newTestUI(t, dir, tabContracts)
	if cmd := press(m, "down", "e"); cmd == nil {
		t.Fatal("e gave no command")
	}
	// Bubble Tea runs the editor; here it "changes" the file, then closes.
	os.WriteFile(filepath.Join(dir, "assay", "docs.yaml"), []byte("id: docs\nname: Docs\nrules: []\n"), 0644)
	m.Update(uiEditedMsg{file: "docs.yaml"})
	if !strings.Contains(m.status, "docs.yaml: 1 problem") {
		t.Fatalf("after editing: %q %q", m.status, m.note)
	}
	m.Update(uiEditedMsg{file: "docs.yaml", err: errors.New("exit status 1")})
	if !strings.Contains(m.status, "the editor failed") {
		t.Fatalf("editor failure: %q", m.status)
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "code --wait")
	if got := editorCommand("a.yaml").Args; strings.Join(got, " ") != "code --wait a.yaml" {
		t.Errorf("editor command %q", got)
	}
	t.Setenv("VISUAL", "nano")
	if got := editorCommand("a.yaml").Args; strings.Join(got, " ") != "nano a.yaml" {
		t.Errorf("VISUAL: %q", got)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := editorCommand("a.yaml").Args; strings.Join(got, " ") != "vi a.yaml" {
		t.Errorf("fallback: %q", got)
	}
}

func TestUICheckAndRecheck(t *testing.T) {
	dir := uiProject(t)
	m := newTestUI(t, dir, tabContracts)
	press(m, "end")
	if target := m.target(); target.contract != "docs" {
		t.Fatalf("selected %+v", target)
	}
	cmd := press(m, "c")
	if cmd == nil || !strings.Contains(m.busy, "checking docs@1") {
		t.Fatalf("c: busy %q", m.busy)
	}
	press(m, "c")
	if !strings.Contains(m.status, "wait for the check") {
		t.Fatalf("second check while busy: %q", m.status)
	}
	m.Update(cmd())
	if m.busy != "" || !strings.Contains(ansi.Strip(m.note), "PASSED · docs@1 · run") || !strings.HasPrefix(m.page().title, "run ") {
		t.Fatalf("after check: %q on %q", m.note, m.page().title)
	}
	if runs, _ := m.data.s.service.ListRuns(); len(runs) != 2 {
		t.Fatalf("%d runs", len(runs))
	}

	// Recheck from the run's page: it stays there and shows attempt 2.
	cmd = press(m, "r")
	m.Update(cmd())
	if !strings.Contains(ansi.Strip(m.note), "attempt 2") || !strings.Contains(ansi.Strip(m.render()), "attempt 2 · passed") {
		t.Fatalf("after recheck: %q\n%s", m.note, ansi.Strip(m.render()))
	}
	if !strings.HasPrefix(m.page().title, "run ") || len(m.stack) != 2 {
		t.Fatalf("recheck moved to %q", m.page().title)
	}

	// Keys that need something else say what.
	press(m, "esc", "tab", "p")
	if !strings.Contains(m.status, "select one on the Contracts tab") {
		t.Fatalf("p on runs: %q", m.status)
	}
	if keys := m.keys(); !strings.HasPrefix(keys, "r recheck · enter open") {
		t.Errorf("runs keys: %q", keys)
	}
}

func TestContractDiff(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init")
	stdout, _, code := runIn(dir, false, "contract", "diff", "assay/example.yaml")
	if code != ExitNegative || !strings.HasPrefix(stdout, "--- (not published)\n+++ assay/example.yaml\n+ version: 1\n") {
		t.Fatalf("unpublished: exit %d\n%s", code, stdout)
	}
	mustRun(t, dir, "contract", "publish", "assay/example.yaml", "--yes")
	stdout, _, code = runIn(dir, false, "contract", "diff", "assay/example.yaml")
	if code != ExitOK || stdout != "example@1 unchanged: the file matches the latest published version\n" {
		t.Fatalf("unchanged: exit %d\n%s", code, stdout)
	}

	path := filepath.Join(dir, "assay", "example.yaml")
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "path: README.md", "path: README.txt", 1)), 0644)
	stdout, _, code = runIn(dir, false, "contract", "diff", "assay/example.yaml")
	want := regexp.MustCompile(`^--- example@1 \(published [^)]+\)\n\+\+\+ assay/example.yaml\n  …\n    - id: readme\n(.*\n)*-     path: README.md\n\+     path: README.txt\n$`)
	if code != ExitNegative || !want.MatchString(stdout) {
		t.Fatalf("changed: exit %d\n%s", code, stdout)
	}
	stdout, _, code = runIn(dir, false, "contract", "diff", "assay/example.yaml", "--json")
	if code != ExitNegative || !strings.Contains(stdout, `"latest": 1`) || !strings.Contains(stdout, `"changed": true`) || !strings.Contains(stdout, `"+     path: README.txt"`) {
		t.Fatalf("json: exit %d\n%s", code, stdout)
	}

	os.WriteFile(path, []byte("id: example\nname: x\nrules: []\n"), 0644)
	if _, _, code = runIn(dir, false, "contract", "diff", "assay/example.yaml"); code != ExitNegative {
		t.Fatalf("invalid file: exit %d", code)
	}
	if _, _, code = runIn(dir, false, "contract", "diff"); code != ExitUsage {
		t.Fatalf("no file: exit %d", code)
	}
}

func TestTrimDiff(t *testing.T) {
	var a, b []string
	for i := range 20 {
		a = append(a, fmt.Sprint(i))
		b = append(b, fmt.Sprint(i))
	}
	b[10] = "ten"
	var got []string
	for _, line := range trimDiff(lineDiff(a, b)) {
		got = append(got, line.String())
	}
	want := ". ,  7,  8,  9,- 10,+ ten,  11,  12,  13,. "
	if strings.Join(got, ",") != want {
		t.Errorf("got %q", strings.Join(got, ","))
	}
}
