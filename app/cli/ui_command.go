package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"assay/project"
)

var uiCommand = command{
	name:    "ui",
	args:    "[run]",
	summary: "Browse contracts and runs in a full-screen terminal UI",
	details: `
Opens a full-screen view of this project: contract files and whether each is
valid and published, published contracts with their versions and rules, and
runs with every attempt, result, and piece of evidence. With a run ID (any
unique prefix), opens that run. Running "assay" with no arguments at a
terminal does the same as "assay ui".

The UI only shows what assay has recorded; it changes nothing. Press ? in it
for keys.

Flags:
` + projectFlagHelp + `

Needs a terminal on both stdin and stdout; exits 64 otherwise.
`,
	run: runUI,
}

func runUI(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	projectDir := fs.String("project", "", "")
	ids, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(ids) > 1 {
		fmt.Fprintln(e.Stderr, "assay ui: name at most one run")
		return ExitUsage
	}
	if !e.Terminal {
		fmt.Fprintln(e.Stderr, "assay ui: needs a terminal; use the commands instead (assay --help)")
		return ExitUsage
	}
	if *projectDir == "" {
		if _, err := project.Find(e.Dir); errors.Is(err, project.ErrNotFound) {
			return offerInit(e, cmd)
		}
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	model := newUIModel(&uiData{s: s}, tabContracts)
	model.welcome = model.data.welcome()
	if len(ids) == 1 {
		run, code := findRun(e, cmd, s, ids[0])
		if code != ExitOK {
			return code
		}
		model = newUIModel(&uiData{s: s}, tabRuns)
		page, err := model.data.runPage(run.ID)
		if err != nil {
			fmt.Fprintf(e.Stderr, "assay ui: %v\n", err)
			return ExitOperational
		}
		model.stack = append(model.stack, page)
	}
	return runProgram(e, model)
}

// runProgram runs the UI until it quits. Bubble Tea restores the terminal
// on the way out, after a panic too.
func runProgram(e Env, model tea.Model) int {
	options := []tea.ProgramOption{}
	if e.Context != nil {
		options = append(options, tea.WithContext(e.Context))
	}
	if _, err := tea.NewProgram(model, options...).Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		fmt.Fprintf(e.Stderr, "assay ui: %v\n", err)
		return ExitOperational
	}
	return ExitOK
}

// offerInit asks, outside a project, whether to create one here, then
// opens it.
func offerInit(e Env, cmd *command) int {
	fmt.Fprintf(e.Stderr, "There's no assay project in %s or above it.\nCreate one here with \"assay init\"? [y/N] ", e.Dir)
	answer, err := readLine(e)
	answer = strings.TrimSpace(answer)
	if err != nil || (answer != "y" && answer != "Y" && answer != "yes") {
		fmt.Fprintln(e.Stderr, "Nothing created. Run \"assay init\" in your project when you're ready.")
		return ExitUsage
	}
	if code := runInit(e, &initCommand, nil); code != ExitOK {
		return code
	}
	return runUI(e, cmd, nil)
}

func readLine(e Env) (string, error) {
	if e.ReadLine == nil {
		return "", errors.New("no input")
	}
	return e.ReadLine()
}
