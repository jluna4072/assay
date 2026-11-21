package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harness/backend"
	"harness/project"
	"harness/storage"
)

const projectFlagHelp = `  --project <dir>   Use the project rooted at <dir> instead of searching
                    upward from the working directory for .harness/`

const exampleContract = `# An example harness contract. Edit it, then:
#   harness contract validate harness/example.yaml
#   harness contract publish harness/example.yaml
version: 1
id: example
name: Example contract
rules:
  - id: readme
    criterion: The project has a README
    check: file_exists
    path: README.md
`

var initCommand = command{
	name:    "init",
	args:    "[dir]",
	summary: "Create a harness project in dir (default: the working directory)",
	details: `
Creates .harness/ (local state: the database of published contracts and runs,
and config.yaml, where you approve commands contracts may run) and harness/
(contract files you commit), with an example contract. Inside a git
repository, adds .harness/ to .gitignore. Running it again changes nothing
that already exists.

Flags:
  --json   Print one JSON object instead of text
`,
	run: runInit,
}

// session is an open project: its location, database, and backend service.
type session struct {
	project project.Project
	store   *storage.SQLite
	service *backend.Service
}

func (s *session) Close() { s.store.Close() }

// openProject finds the project (or opens the one --project names) and its
// database. On failure it reports the problem and returns the exit code.
func openProject(e Env, cmd *command, dir string) (*session, int) {
	var p project.Project
	var err error
	if dir != "" {
		p, err = project.Open(resolve(e, dir))
	} else {
		p, err = project.Find(e.Dir)
	}
	if errors.Is(err, project.ErrNotFound) {
		fmt.Fprintf(e.Stderr, "harness %s: %v\nRun \"harness init\" to create one.\n", cmd.name, err)
		return nil, ExitUsage
	}
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return nil, ExitOperational
	}
	store, err := storage.Open(p.DatabasePath())
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: open %s: %v\n", cmd.name, p.DatabasePath(), err)
		return nil, ExitOperational
	}
	service := backend.NewService(store)
	service.LoadConfig = project.LoadConfig
	service.Keys = newKeys(e, true)
	return &session{project: p, store: store, service: service}, ExitOK
}

// resolve interprets a command-line path relative to the working directory.
func resolve(e Env, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(e.Dir, path)
}

type initJSON struct {
	Schema    int      `json:"schema"`
	Root      string   `json:"root"`
	Created   []string `json:"created"`
	Existing  []string `json:"existing"`
	Gitignore string   `json:"gitignore"` // "added", "present", or "not a git repository"
}

func runInit(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	positional, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(positional) > 1 {
		fmt.Fprintf(e.Stderr, "harness init: expected at most one directory, got %d\n", len(positional))
		return ExitUsage
	}
	dir := e.Dir
	if len(positional) == 1 {
		dir = resolve(e, positional[0])
	}
	root, err := filepath.Abs(dir)
	if err == nil {
		var info os.FileInfo
		if info, err = os.Stat(root); err == nil && !info.IsDir() {
			err = fmt.Errorf("%s is not a directory", dir)
		}
	}
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness init: %v\n", err)
		return ExitOperational
	}
	p := project.Project{Root: root}
	out := initJSON{Schema: JSONSchema, Root: root, Created: []string{}, Existing: []string{}}
	fail := func(err error) int {
		fmt.Fprintf(e.Stderr, "harness init: %v\n", err)
		return ExitOperational
	}
	note := func(rel string, existed bool) {
		if existed {
			out.Existing = append(out.Existing, rel)
		} else {
			out.Created = append(out.Created, rel)
		}
	}

	dbRel := filepath.Join(project.StateDir, project.DatabaseFile)
	_, statErr := os.Stat(p.DatabasePath())
	store, err := storage.Open(p.DatabasePath()) // creates .harness/ and the schema
	if err != nil {
		return fail(err)
	}
	store.Close()
	note(dbRel, statErr == nil)

	configRel := filepath.Join(project.StateDir, project.ConfigFile)
	if _, err := os.Stat(p.ConfigPath()); err == nil {
		note(configRel, true)
	} else {
		if err := os.WriteFile(p.ConfigPath(), []byte(project.ConfigTemplate), 0600); err != nil {
			return fail(err)
		}
		note(configRel, false)
	}

	contractsRel := project.ContractsDir + string(filepath.Separator)
	exampleRel := filepath.Join(project.ContractsDir, project.ExampleFile)
	existing, err := contractFiles(p.ContractsPath())
	if err != nil {
		return fail(err)
	}
	switch {
	case len(existing) > 0:
		note(contractsRel, true)
	default:
		if err := os.MkdirAll(p.ContractsPath(), 0755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(filepath.Join(root, exampleRel), []byte(exampleContract), 0644); err != nil {
			return fail(err)
		}
		note(exampleRel, false)
	}

	out.Gitignore = "not a git repository"
	if project.InGitRepo(root) {
		added, err := project.EnsureIgnored(root)
		if err != nil {
			return fail(err)
		}
		out.Gitignore = "present"
		if added {
			out.Gitignore = "added"
		}
	}

	if *asJSON {
		writeJSON(e.Stdout, out)
		return ExitOK
	}
	for _, rel := range out.Created {
		fmt.Fprintf(e.Stdout, "created  %s\n", rel)
	}
	for _, rel := range out.Existing {
		fmt.Fprintf(e.Stdout, "exists   %s\n", rel)
	}
	if out.Gitignore == "added" {
		fmt.Fprintf(e.Stdout, "added    /%s/ to .gitignore\n", project.StateDir)
	}
	if len(out.Created) == 0 {
		fmt.Fprintf(e.Stdout, "harness project already initialized in %s\n", root)
		return ExitOK
	}
	fmt.Fprintf(e.Stdout, "Initialized harness project in %s\n", root)
	if len(existing) == 0 {
		fmt.Fprintf(e.Stdout, "Next: harness contract validate %s\n", exampleRel)
	}
	return ExitOK
}

// contractFiles lists YAML files directly inside dir; a missing dir is empty.
func contractFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			files = append(files, name)
		}
	}
	return files, nil
}
