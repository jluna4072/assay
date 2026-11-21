package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// typeText types into the task form.
func typeText(m *uiModel, text string) {
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// startUIWork opens the task form on the published readme contract,
// starts the task, and returns the command that waits for the run to end.
func startUIWork(t *testing.T, m *uiModel, dir string) tea.Cmd {
	t.Helper()
	press(m, "end")
	if target := m.target(); target.contract != "readme" {
		t.Fatalf("selected %+v", target)
	}
	press(m, "w")
	if m.task == nil {
		t.Fatalf("no task form: %q", m.status)
	}
	golden(t, "ui-task.txt", dir, screen(t, m, dir))
	press(m, "enter")
	if m.task == nil || m.status != "say what the worker should do" {
		t.Fatalf("empty task: %q", m.status)
	}
	typeText(m, "Write a README")
	cmd := m.taskKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // Update would add the poll to it
	if cmd == nil || m.task != nil || m.work == nil {
		t.Fatalf("enter didn't start work: %q", m.status)
	}
	msg := cmd()
	started, ok := msg.(uiWorkStartedMsg)
	if !ok {
		t.Fatalf("got %#v", msg)
	}
	wait := m.workStarted(started)
	if !strings.HasPrefix(m.page().title, "work ") || m.work.run != started.run {
		t.Fatalf("on %q", m.page().title)
	}
	return wait
}

func TestUIWorkAccepted(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo writing; echo hello > README.md`)
	m := newTestUI(t, dir, tabContracts)
	wait := startUIWork(t, m, dir)
	if m.tick() == nil {
		t.Fatal("no polling while work runs")
	}
	if cmd := m.workFinished(wait().(uiWorkFinishedMsg)); cmd != nil {
		t.Fatal("finishing quit the UI")
	}
	if !strings.Contains(ansi.Strip(m.note), "ACCEPTED · run") || m.work != nil {
		t.Fatalf("note %q", m.note)
	}
	text := ansi.Strip(m.render())
	for _, want := range []string{"· writer · accepted · attempt 1 of 3", "task: Write a README", "attempt 1 of 3 · running writer", "│ writing", "✓ readme"} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	press(m, "tab")
	text = ansi.Strip(m.render())
	if !strings.Contains(text, "attempt 1 · passed") || m.page().cursor < 0 {
		t.Fatalf("results:\n%s", text)
	}
	m.ticking = false
	if m.tick() != nil {
		t.Error("still polling after the run ended")
	}
}

func TestUIWorkCancelAndQuit(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo started; sleep 30`)
	m := newTestUI(t, dir, tabContracts)
	wait := startUIWork(t, m, dir)
	press(m, "x")
	if m.confirm == nil || !strings.HasPrefix(m.keys(), "y cancel run ") {
		t.Fatalf("x: %q", m.keys())
	}
	press(m, "n")
	if m.confirm != nil || m.note != "still working" {
		t.Fatalf("n: %q", m.note)
	}
	press(m, "x", "y")
	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case msg := <-done:
		m.workFinished(msg.(uiWorkFinishedMsg))
	case <-time.After(15 * time.Second):
		t.Fatal("cancelling didn't stop the worker")
	}
	if !strings.Contains(ansi.Strip(m.note), "CANCELLED") {
		t.Fatalf("note %q", m.note)
	}

	// Quitting while work runs asks first, then cancels it before quitting.
	m = newTestUI(t, dir, tabContracts)
	wait = startUIWork(t, m, dir)
	if cmd := m.key("ctrl+c"); cmd != nil || m.confirm == nil || !m.confirm.quit {
		t.Fatal("ctrl+c quit without asking about the work run")
	}
	press(m, "ctrl+c") // again: yes
	if !m.quitting {
		t.Fatal("second ctrl+c didn't confirm")
	}
	if cmd := m.workFinished(wait().(uiWorkFinishedMsg)); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("didn't quit once the run was cancelled")
	}
}

// TestUIFollowsWorkStartedElsewhere follows a run that "assay work" in
// another process (here, another goroutine) is making.
func TestUIFollowsWorkStartedElsewhere(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "go")
	dir := workCLIProject(t, `cat >/dev/null; echo waiting; while [ ! -f `+gate+` ]; do sleep 0.05; done; echo hello > README.md`)
	done := make(chan int, 1)
	go func() {
		_, _, code := runIn(dir, false, "work", "readme", "--task", "Write a README")
		done <- code
	}()
	m := newTestUI(t, dir, tabRuns)
	deadline := time.Now().Add(10 * time.Second)
	for !m.page().active {
		if time.Now().After(deadline) {
			t.Fatalf("the run never showed up:\n%s", ansi.Strip(m.render()))
		}
		time.Sleep(20 * time.Millisecond)
		m.reload()
	}
	press(m, "enter")
	p := m.page()
	if p.live == nil || !strings.Contains(ansi.Strip(m.render()), "◆ running") {
		t.Fatalf("not a live Work page:\n%s", ansi.Strip(m.render()))
	}
	press(m, "x")
	if !strings.Contains(m.status, "only cancels work started in this window") {
		t.Fatalf("x: %q", m.status)
	}

	os.WriteFile(gate, nil, 0644)
	for !p.live.finished {
		if time.Now().After(deadline) {
			t.Fatalf("never saw the run end:\n%s", ansi.Strip(m.render()))
		}
		time.Sleep(20 * time.Millisecond)
		m.ticked()
	}
	text := ansi.Strip(m.render())
	if !strings.Contains(text, "accepted") || !strings.Contains(text, "│ waiting") {
		t.Fatalf("after the run:\n%s", text)
	}
	if code := <-done; code != ExitOK {
		t.Fatalf("assay work: exit %d", code)
	}
}

// TestUIWorkInATerminal starts a work run from the real binary's UI, sees
// it accepted, and quits.
func TestUIWorkInATerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := workCLIProject(t, `cat >/dev/null; echo writing; echo hello > README.md`)
	bin := filepath.Join(t.TempDir(), "assay")
	if out, err := exec.Command("go", "build", "-o", bin, "assay/cmd/assay").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=1")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	var output safeBuffer
	go io.Copy(&output, terminal)
	waitFor := func(text string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !strings.Contains(ansi.Strip(output.String()), text) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q:\n%q", text, output.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("Published contracts")
	terminal.Write([]byte("Gw"))
	waitFor("Start work on readme@1")
	terminal.Write([]byte("Write a README\r"))
	waitFor("ACCEPTED · run")
	terminal.Write([]byte("qq")) // back to the list, then quit

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
	if out := mustRun(t, dir, "runs"); !strings.Contains(out, "passed") {
		t.Errorf("no accepted run:\n%s", out)
	}
}
