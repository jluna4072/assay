package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"assay/backend"
	"assay/project"
)

var doctorCommand = command{
	name:    "doctor",
	args:    "",
	summary: "Check that assay is set up to run here",
	details: `
Checks what assay needs and says how to fix anything missing: git, the
command sandbox, and, inside a project, its config, the programs its
commands, reviewers, and workers run, reviewer API keys, worker logins
(Codex), and its contract files. Nothing is changed and no model is called;
"assay reviewer test" sends a reviewer real requests.

Flags:
  --json   Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 nothing needs fixing (warnings are allowed), 1 something
needs fixing, 64 usage error.
`,
	run: runDoctor,
}

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorCheck struct {
	Section string       `json:"section"` // "machine" or "project"
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Detail  string       `json:"detail,omitempty"`
	Fix     string       `json:"fix,omitempty"`
}

// codexLoginStatus runs "codex login status". It is a variable so tests
// don't depend on Codex being installed or logged in.
var codexLoginStatus = func(program string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, program, "login", "status").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runDoctor(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	rest, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(e.Stderr, "assay %s: unexpected argument %q\n", cmd.name, rest[0])
		return ExitUsage
	}

	var checks []doctorCheck
	add := func(c doctorCheck) { checks = append(checks, c) }
	machine := func(name string, status doctorStatus, detail, fix string) {
		add(doctorCheck{Section: "machine", Name: name, Status: status, Detail: detail, Fix: fix})
	}

	// This machine.
	if out, err := exec.Command("git", "--version").Output(); err != nil {
		machine("git", doctorFail, "git isn't installed or isn't on PATH; assay work and diff evidence need it", "install it with: xcode-select --install (or brew install git)")
	} else {
		machine("git", doctorOK, strings.TrimPrefix(strings.TrimSpace(string(out)), "git version "), "")
	}
	if err := backend.SandboxAvailable(); err != nil {
		fix := "set \"sandbox: off\" in .assay/config.yaml; commands then run unsandboxed and are marked so"
		if runtime.GOOS != "darwin" {
			fix = "the command sandbox is macOS only for now; " + fix
		}
		machine("command sandbox", doctorWarn, err.Error(), fix)
	} else {
		machine("command sandbox", doctorOK, "sandbox-exec works", "")
	}

	// The project, when there is one.
	var p project.Project
	var err error
	if *projectDir != "" {
		p, err = project.Open(resolve(e, *projectDir))
	} else {
		p, err = project.Find(e.Dir)
	}
	root := ""
	if err != nil {
		status, fix := doctorWarn, "run \"assay init\" in your project to create one"
		if !errors.Is(err, project.ErrNotFound) {
			status, fix = doctorFail, ""
		}
		add(doctorCheck{Section: "project", Name: "project", Status: status, Detail: err.Error(), Fix: fix})
	} else {
		root = p.Root
		checks = append(checks, doctorProject(p)...)
	}

	failed, warned := 0, 0
	for _, c := range checks {
		switch c.Status {
		case doctorFail:
			failed++
		case doctorWarn:
			warned++
		}
	}
	code = ExitOK
	if failed > 0 {
		code = ExitNegative
	}

	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema  int           `json:"schema"`
			Version string        `json:"version"`
			OS      string        `json:"os"`
			Project string        `json:"project,omitempty"`
			OK      bool          `json:"ok"`
			Checks  []doctorCheck `json:"checks"`
		}{JSONSchema, version(), runtime.GOOS + "/" + runtime.GOARCH, root, failed == 0, checks})
		return code
	}

	fmt.Fprintf(e.Stdout, "assay %s · %s/%s\n", version(), runtime.GOOS, runtime.GOARCH)
	section := ""
	for _, c := range checks {
		if c.Section != section {
			section = c.Section
			title := "This machine"
			if section == "project" {
				title = "Project"
				if root != "" {
					title += " " + displayPath(root)
				}
			}
			fmt.Fprintf(e.Stdout, "\n%s\n", title)
		}
		mark := map[doctorStatus]string{doctorOK: "✓", doctorWarn: "!", doctorFail: "✗"}[c.Status]
		line := "  " + mark + " " + c.Name
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		fmt.Fprintln(e.Stdout, line)
		if c.Fix != "" {
			fmt.Fprintf(e.Stdout, "      fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintln(e.Stdout)
	switch {
	case failed > 0:
		fmt.Fprintf(e.Stdout, "%s to fix", plural(failed, "problem"))
		if warned > 0 {
			fmt.Fprintf(e.Stdout, ", %s", plural(warned, "warning"))
		}
		fmt.Fprintln(e.Stdout, ".")
	case warned > 0:
		fmt.Fprintf(e.Stdout, "Ready, with %s.\n", plural(warned, "warning"))
	default:
		fmt.Fprintln(e.Stdout, "Everything's ready.")
	}
	return code
}

// doctorProject checks a project's config, the programs it runs, reviewer
// keys, worker logins, and contract files.
func doctorProject(p project.Project) []doctorCheck {
	var checks []doctorCheck
	add := func(name string, status doctorStatus, detail, fix string) {
		checks = append(checks, doctorCheck{Section: "project", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	configName := filepath.Join(project.StateDir, project.ConfigFile)

	if project.InGitRepo(p.Root) {
		add("git repository", doctorOK, "", "")
	} else {
		add("git repository", doctorWarn, "not in a git repository; assay work needs one", "run: git init && git add -A && git commit -m start")
	}

	config, err := project.LoadConfig(p.Root)
	if err != nil {
		add(configName, doctorFail, strings.TrimPrefix(err.Error(), "invalid config:\n  "), "fix the lines above in "+configName)
		return checks
	}
	if _, err := os.Stat(p.ConfigPath()); err != nil {
		add(configName, doctorWarn, "missing; using the defaults", "run \"assay init\" again to write a commented one")
	} else {
		add(configName, doctorOK, fmt.Sprintf("%s, %s, %s", plural(len(config.Commands), "command"), plural(len(config.Reviewers), "reviewer"), plural(len(config.Workers), "worker")), "")
	}
	if !config.Sandbox {
		add("sandbox", doctorWarn, "off in "+configName+"; commands run unsandboxed", "set \"sandbox: on\" if this machine has the sandbox")
	}

	// program reports whether a config entry's program can be found.
	program := func(kind, name string, run []string) (string, bool) {
		if len(run) == 0 {
			return "", false
		}
		path, err := exec.LookPath(run[0])
		if err != nil {
			add(kind+" "+name, doctorFail, fmt.Sprintf("%s isn't installed or isn't on PATH", run[0]), "install it, or change run: for "+name+" in "+configName)
			return "", false
		}
		return path, true
	}

	for _, name := range sortedKeys(config.Commands) {
		if _, ok := program("command", name, config.Commands[name].Run); ok {
			add("command "+name, doctorOK, describeRun(config.Commands[name].Run), "")
		}
	}

	defaultReviewer, _ := config.Reviewer("")
	for _, name := range sortedKeys(config.Reviewers) {
		spec := config.Reviewers[name]
		label := name
		if name == defaultReviewer.Name && len(config.Reviewers) > 1 {
			label += " (default)"
		}
		if spec.Protocol == backend.CommandProtocol {
			if _, ok := program("reviewer", label, spec.Command.Run); ok {
				add("reviewer "+label, doctorOK, describeRun(spec.Command.Run), "")
			}
			continue
		}
		d := spec.Settings()
		switch source := keySource(spec); source {
		case "missing":
			add("reviewer "+label, doctorFail, "no API key for "+d.Host(), fmt.Sprintf("assay reviewer login %s (or set %s)", name, d.KeyEnv))
		default:
			add("reviewer "+label, doctorOK, fmt.Sprintf("%s, key: %s", d.Model, source), "")
		}
	}
	if len(config.Reviewers) > 1 && config.DefaultReviewer == "" {
		add("default reviewer", doctorWarn, "several reviewers and no default_reviewer", "set default_reviewer in "+configName+", or name a reviewer in each review rule")
	}

	for _, name := range sortedKeys(config.Workers) {
		spec := config.Workers[name]
		path, ok := program("worker", name, spec.Command.Run)
		if !ok {
			continue
		}
		if filepath.Base(path) != "codex" {
			add("worker "+name, doctorOK, describeRun(spec.Command.Run), "")
			continue
		}
		if out, err := codexLoginStatus(path); err != nil {
			detail := "Codex isn't logged in"
			if out != "" {
				detail += ": " + oneLine(out)
			}
			add("worker "+name, doctorFail, detail, "run: codex login")
		} else {
			add("worker "+name, doctorOK, "codex · "+oneLine(out), "")
		}
	}
	if len(config.Workers) > 1 && config.DefaultWorker == "" {
		add("default worker", doctorWarn, "several workers and no default_worker", "set default_worker in "+configName+", or pass --worker")
	}

	files, err := contractFiles(p.ContractsPath())
	switch {
	case err != nil:
		add("contracts", doctorFail, err.Error(), "")
	case len(files) == 0:
		add("contracts", doctorWarn, "no contract files in "+project.ContractsDir+"/", "write one (see docs/getting-started.md), then: assay contract publish <file>")
	default:
		check := projectConfig{config: &config}
		bad := 0
		for _, file := range files {
			rel := filepath.Join(project.ContractsDir, file)
			result, err := check.check(filepath.Join(p.ContractsPath(), file))
			if err != nil || !result.Valid() {
				bad++
				detail := "can't be read"
				if err == nil {
					detail = plural(len(result.Problems), "problem")
				}
				add("contract "+rel, doctorFail, detail, "assay contract validate "+rel)
			}
		}
		if bad == 0 {
			add("contracts", doctorOK, plural(len(files), "contract file")+", all valid", "")
		}
	}
	return checks
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// displayPath shortens a path in the home directory to ~/...
func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// describeRun shows a program and its arguments on one short line.
func describeRun(run []string) string {
	text := []rune(oneLine(strings.Join(run, " ")))
	if len(text) > 60 {
		return string(text[:59]) + "…"
	}
	return string(text)
}
