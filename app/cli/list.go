package cli

import (
	"flag"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"assay/backend"
	"assay/contractfile"
)

var listCommand = command{
	name:    "contract list",
	args:    "",
	summary: "List contract files and published contracts",
	details: `
Lists the contract files in the project's assay/ directory, with whether
each is valid and whether it is published as is, changed since its latest
version, or not published, including the suites in assay/suites/; then the
published contracts with their latest version.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `
`,
	run: runList,
}

type listFileJSON struct {
	Path     string                 `json:"path"`
	Valid    bool                   `json:"valid"`
	Contract *contractSummaryJSON   `json:"contract,omitempty"`
	Problems []contractfile.Problem `json:"problems,omitempty"`
	Error    string                 `json:"error,omitempty"`
	// Published is "same" (matches the latest version), "changed", or
	// "unpublished", for valid files.
	Published string `json:"published,omitempty"`
	Latest    int    `json:"latest,omitempty"`
	// Suite lists a suite folder's contracts.
	Suite []string `json:"suite,omitempty"`
}

type listContractJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Latest   int    `json:"latest"`
	Versions int    `json:"versions"`
	Rules    int    `json:"rules"`
}

func runList(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	rest, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(e.Stderr, "assay contract list: unexpected argument %q\n", rest[0])
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	dir := s.project.ContractsPath()
	names, err := contractFiles(dir)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay contract list: %v\n", err)
		return ExitOperational
	}
	published, err := s.service.ListContracts()
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay contract list: %v\n", err)
		return ExitOperational
	}
	latest := map[string]backend.PublishedContract{}
	contracts := []listContractJSON{}
	for _, c := range published {
		versions, err := s.service.ListVersions(c.ID)
		if err != nil {
			fmt.Fprintf(e.Stderr, "assay contract list: %v\n", err)
			return ExitOperational
		}
		if len(versions) == 0 {
			continue
		}
		v := versions[len(versions)-1]
		latest[c.ID] = v
		contracts = append(contracts, listContractJSON{ID: c.ID, Name: v.Contract.Name, Latest: v.Version, Versions: len(versions), Rules: len(v.Contract.Rules)})
	}

	config := findConfig(s.project.Root)
	files := []listFileJSON{}
	for _, name := range names {
		file := listFileJSON{Path: filepath.Join(filepath.Base(dir), name)}
		result, err := config.check(filepath.Join(dir, name))
		switch {
		case err != nil:
			file.Error = err.Error()
		case !result.Valid():
			file.Problems = result.Problems
		default:
			c := result.Contract
			file.Valid = true
			file.Contract = &contractSummaryJSON{ID: c.ID, Name: c.Name, Rules: len(c.Rules)}
			file.Suite = result.Members
			file.Published = "unpublished"
			if p, ok := latest[c.ID]; ok {
				file.Latest = p.Version
				file.Published = "changed"
				if backend.SameContent(p.Contract, c) {
					file.Published = "same"
				}
			}
		}
		files = append(files, file)
	}

	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema    int                `json:"schema"`
			Files     []listFileJSON     `json:"files"`
			Contracts []listContractJSON `json:"contracts"`
		}{JSONSchema, files, contracts})
		return ExitOK
	}
	fmt.Fprintf(e.Stdout, "Contract files in %s/\n", filepath.Base(dir))
	if len(files) == 0 {
		fmt.Fprintln(e.Stdout, "  none yet")
	}
	table := tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range files {
		switch {
		case f.Error != "":
			fmt.Fprintf(table, "  ! %s\t%s\n", f.Path, oneLine(f.Error))
		case !f.Valid:
			fmt.Fprintf(table, "  ✗ %s\t%s\n", f.Path, plural(len(f.Problems), "problem"))
		default:
			state := "not published"
			switch f.Published {
			case "same":
				state = fmt.Sprintf("published as v%d", f.Latest)
			case "changed":
				state = fmt.Sprintf("changed since v%d", f.Latest)
			}
			size := plural(f.Contract.Rules, "rule")
			if len(f.Suite) > 0 {
				size = "suite of " + plural(len(f.Suite), "contract") + ", " + size
			}
			fmt.Fprintf(table, "  ✓ %s\t%s · %s · %s\n", f.Path, f.Contract.ID, size, state)
		}
	}
	table.Flush()
	fmt.Fprintln(e.Stdout, "\nPublished contracts")
	if len(contracts) == 0 {
		fmt.Fprintln(e.Stdout, "  none yet")
	}
	table = tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range contracts {
		fmt.Fprintf(table, "  %s\tv%d\t%s\t%s\n", c.ID, c.Latest, plural(c.Rules, "rule"), c.Name)
	}
	table.Flush()
	return ExitOK
}
