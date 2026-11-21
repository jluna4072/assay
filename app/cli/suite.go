package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"assay/contractfile"
)

// suitesDir is where a project keeps the suites added to it, inside assay/.
const suitesDir = "suites"

var suiteAddCommand = command{
	name:    "suite add",
	args:    "<name | folder>",
	summary: "Copy a suite of contracts into this project",
	details: `
A suite is a folder of contract files with a suite.yaml that lists them. It
is validated, published, and checked as one contract whose rules are all of
its contracts' rules, named <contract>/<rule>, so it passes only when every
one passes. Keep suites in a library folder and add them to any project.

suite add copies a suite into assay/suites/<id>/, to commit with the
project, then validates it here. A name is looked up in your suite library
(` + "`assay suite list`" + ` shows where); anything with a "/" is a folder. Then publish
it like a contract: assay contract publish assay/suites/<id>

Flags:
  --force           Replace a suite already added (to take a newer version
                    from the library); publish it again to use the changes
` + projectFlagHelp + `

Exit codes: 0 added and valid here, 1 added but it has problems here (for
example, commands this project hasn't approved), 2 couldn't copy it, 64
usage error or no such suite.
`,
	run: runSuiteAdd,
}

var suiteListCommand = command{
	name:    "suite list",
	args:    "",
	summary: "List the suites in your suite library",
	details: `
Lists the suites in your suite library: the folder ASSAY_SUITES names, or
$XDG_CONFIG_HOME/assay/suites (~/.config/assay/suites by default). Add one
to a project with: assay suite add <name>

Flags:
  --json   Print one JSON object instead of text
`,
	run: runSuiteList,
}

// suiteLibrary is the folder suites are found in by name.
func suiteLibrary() string {
	if dir := os.Getenv("ASSAY_SUITES"); dir != "" {
		return dir
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home, _ := os.UserHomeDir()
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "assay", "suites")
}

func runSuiteAdd(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	projectDir := fs.String("project", "", "")
	refs, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(refs) != 1 {
		fmt.Fprintln(e.Stderr, "assay suite add: name one suite, or a suite folder")
		return ExitUsage
	}
	source := refs[0]
	if !strings.ContainsRune(source, '/') && !strings.HasPrefix(source, ".") {
		source = filepath.Join(suiteLibrary(), source)
	} else {
		source = resolve(e, source)
	}
	if !contractfile.IsSuite(source) {
		fmt.Fprintf(e.Stderr, "assay suite add: %s isn't a suite: there's no %s in it\n", source, contractfile.SuiteFile)
		return ExitUsage
	}
	if filepath.Base(source) == contractfile.SuiteFile {
		source = filepath.Dir(source)
	}
	id, files, err := contractfile.SuiteContracts(source)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay suite add: %v\n", err)
		return ExitOperational
	}
	if !validName(id) {
		fmt.Fprintf(e.Stderr, "assay suite add: %s needs an id made of letters, digits, '.', '_', or '-'\n", filepath.Join(source, contractfile.SuiteFile))
		return ExitUsage
	}

	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()
	rel := filepath.Join(filepath.Base(s.project.ContractsPath()), suitesDir, id)
	dest := filepath.Join(s.project.ContractsPath(), suitesDir, id)
	if _, err := os.Stat(dest); err == nil {
		if !*force {
			fmt.Fprintf(e.Stderr, "assay suite add: %s is already in this project at %s. Pass --force to replace it.\n", id, rel)
			return ExitUsage
		}
		if err := os.RemoveAll(dest); err != nil {
			fmt.Fprintf(e.Stderr, "assay suite add: %v\n", err)
			return ExitOperational
		}
	}
	// Copy suite.yaml and the contracts it lists, nothing else.
	if err := os.MkdirAll(dest, 0755); err != nil {
		fmt.Fprintf(e.Stderr, "assay suite add: %v\n", err)
		return ExitOperational
	}
	for _, name := range append([]string{contractfile.SuiteFile}, files...) {
		if !validName(strings.TrimSuffix(name, ".yaml")) {
			continue // CheckSuite reports it
		}
		data, err := os.ReadFile(filepath.Join(source, name))
		if errors.Is(err, os.ErrNotExist) {
			continue // CheckSuite reports it
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(dest, name), data, 0644)
		}
		if err != nil {
			fmt.Fprintf(e.Stderr, "assay suite add: copy %s: %v\n", name, err)
			return ExitOperational
		}
	}

	result, err := findConfig(s.project.Root).check(dest)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay suite add: %v\n", err)
		return ExitOperational
	}
	fmt.Fprintf(e.Stdout, "added %s from %s\n", rel, source)
	if !result.Valid() {
		printProblems(e, rel, result.Problems)
		fmt.Fprintf(e.Stdout, "Fix these here (approve commands in .assay/config.yaml, configure reviewers), then: assay contract publish %s\n", rel)
		return ExitNegative
	}
	fmt.Fprintf(e.Stdout, "✓ %s: %s, %s, %s\n", rel, result.Contract.ID, plural(len(result.Members), "contract"), plural(len(result.Contract.Rules), "rule"))
	fmt.Fprintf(e.Stdout, "Next: assay contract publish %s\n", rel)
	return ExitOK
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == ""
}

type suiteJSON struct {
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Path      string                 `json:"path"`
	Valid     bool                   `json:"valid"`
	Contracts []string               `json:"contracts,omitempty"`
	Rules     int                    `json:"rules,omitempty"`
	Problems  []contractfile.Problem `json:"problems,omitempty"`
}

func runSuiteList(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	rest, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(e.Stderr, "assay suite list: unexpected argument %q\n", rest[0])
		return ExitUsage
	}
	library := suiteLibrary()
	entries, err := os.ReadDir(library)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(e.Stderr, "assay suite list: %v\n", err)
		return ExitOperational
	}
	suites := []suiteJSON{}
	for _, entry := range entries {
		dir := filepath.Join(library, entry.Name())
		if !entry.IsDir() || !contractfile.IsSuite(dir) {
			continue
		}
		// Checked without a project's config: commands and reviewers are
		// checked when it's added to one.
		result, err := contractfile.CheckSuite(dir)
		item := suiteJSON{Path: dir, Valid: err == nil && result.Valid(), Problems: result.Problems}
		if item.Valid {
			item.ID, item.Name, item.Contracts, item.Rules = result.Contract.ID, result.Contract.Name, result.Members, len(result.Contract.Rules)
		}
		suites = append(suites, item)
	}
	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema  int         `json:"schema"`
			Library string      `json:"library"`
			Suites  []suiteJSON `json:"suites"`
		}{JSONSchema, library, suites})
		return ExitOK
	}
	fmt.Fprintf(e.Stdout, "Suites in %s\n", library)
	if len(suites) == 0 {
		fmt.Fprintln(e.Stdout, "  none yet: make a folder here with a suite.yaml and its contract files")
		return ExitOK
	}
	for _, suite := range suites {
		if !suite.Valid {
			fmt.Fprintf(e.Stdout, "  ✗ %s: %s\n", filepath.Base(suite.Path), plural(len(suite.Problems), "problem"))
			for _, p := range suite.Problems {
				fmt.Fprintf(e.Stdout, "      %s\n", p.In(suite.Path))
			}
			continue
		}
		fmt.Fprintf(e.Stdout, "  %s  %s · %s (%s) · %s\n", suite.ID, suite.Name, plural(len(suite.Contracts), "contract"), strings.Join(suite.Contracts, ", "), plural(suite.Rules, "rule"))
	}
	return ExitOK
}
