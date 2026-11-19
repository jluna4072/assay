package cli

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"harness/backend"
	"harness/contractfile"
	"harness/project"
)

var publishCommand = command{
	name:    "contract publish",
	args:    "<file>",
	summary: "Freeze a contract file as its next published version",
	details: `
Validates the file and stores it as the contract's next version (docs@1,
docs@2, ...). Published versions never change; runs stay tied to the version
they started with. If the file matches the latest version, nothing is
published.

Publishing is a decision for the person who owns the contract, so it refuses
to run without a terminal (for example, from a script or an agent) unless
--yes is passed.

Flags:
  --yes             Publish without a terminal
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 published or unchanged, 1 invalid file, 2 unreadable file or
database, 64 usage error or no confirmation.
`,
	run: runPublish,
}

var versionsCommand = command{
	name:    "contract versions",
	args:    "<id>",
	summary: "List a contract's published versions",
	details: `
Lists every published version of a contract, newest first.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `
`,
	run: runVersions,
}

var showCommand = command{
	name:    "contract show",
	args:    "<id>[@version]",
	summary: "Print a published version as a contract file",
	details: `
Prints a published version in the contract file format. Without @version,
prints the latest version.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `
`,
	run: runShow,
}

type publishJSON struct {
	Schema      int                    `json:"schema"`
	Published   bool                   `json:"published"`
	Contract    string                 `json:"contract,omitempty"`
	Version     int                    `json:"version,omitempty"`
	Rules       int                    `json:"rules,omitempty"`
	PublishedAt *time.Time             `json:"publishedAt,omitempty"`
	Problems    []contractfile.Problem `json:"problems,omitempty"`
}

func runPublish(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	yes := fs.Bool("yes", false, "")
	projectDir := fs.String("project", "", "")
	files, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(files) != 1 {
		fmt.Fprintln(e.Stderr, "harness contract publish: name exactly one contract file")
		return ExitUsage
	}
	path := files[0]

	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	result, err := contractfile.Check(resolve(e, path))
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness contract publish: %v\n", err)
		return ExitOperational
	}
	if result.Valid() {
		config, err := project.LoadConfig(s.project.Root)
		if err != nil {
			fmt.Fprintf(e.Stderr, "harness contract publish: %v\n", err)
			return ExitOperational
		}
		result.AddValidation(backend.ValidateCommands(result.Contract, config))
	}
	if !result.Valid() {
		if *asJSON {
			writeJSON(e.Stdout, publishJSON{Schema: JSONSchema, Problems: result.Problems})
		} else {
			printProblems(e, path, result.Problems)
			fmt.Fprintln(e.Stdout, "nothing published")
		}
		return ExitNegative
	}
	if !e.Interactive && !*yes {
		fmt.Fprintln(e.Stderr, "harness contract publish: publishing needs confirmation. Run it in a terminal, or pass --yes.")
		return ExitUsage
	}

	published, changed, err := s.service.PublishContent(result.Contract)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness contract publish: %v\n", err)
		return ExitOperational
	}
	if *asJSON {
		writeJSON(e.Stdout, publishJSON{
			Schema: JSONSchema, Published: changed, Contract: published.Contract.ID,
			Version: published.Version, Rules: len(published.Contract.Rules), PublishedAt: &published.PublishedAt,
		})
		return ExitOK
	}
	ref := fmt.Sprintf("%s@%d", published.Contract.ID, published.Version)
	if changed {
		fmt.Fprintf(e.Stdout, "published %s (%s)\n", ref, plural(len(published.Contract.Rules), "rule"))
	} else {
		fmt.Fprintf(e.Stdout, "%s unchanged: the file matches the latest published version\n", ref)
	}
	return ExitOK
}

type versionJSON struct {
	Version     int       `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	Name        string    `json:"name"`
	Rules       int       `json:"rules"`
}

func runVersions(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	ids, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(ids) != 1 {
		fmt.Fprintln(e.Stderr, "harness contract versions: name exactly one contract ID")
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	versions, err := s.service.ListVersions(ids[0])
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness contract versions: %v\n", err)
		return ExitOperational
	}
	if len(versions) == 0 {
		fmt.Fprintf(e.Stderr, "harness contract versions: no published contract %q\n", ids[0])
		return ExitUsage
	}
	if *asJSON {
		list := make([]versionJSON, 0, len(versions))
		for i := len(versions) - 1; i >= 0; i-- {
			v := versions[i]
			list = append(list, versionJSON{Version: v.Version, PublishedAt: v.PublishedAt, Name: v.Contract.Name, Rules: len(v.Contract.Rules)})
		}
		writeJSON(e.Stdout, struct {
			Schema   int           `json:"schema"`
			Contract string        `json:"contract"`
			Versions []versionJSON `json:"versions"`
		}{JSONSchema, ids[0], list})
		return ExitOK
	}
	latest := versions[len(versions)-1]
	fmt.Fprintf(e.Stdout, "%s (%s)\n", ids[0], latest.Contract.Name)
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		marker := ""
		if i == len(versions)-1 {
			marker = "  latest"
		}
		fmt.Fprintf(e.Stdout, "  @%-3d %s  %s%s\n", v.Version, v.PublishedAt.Local().Format("2006-01-02 15:04"), plural(len(v.Contract.Rules), "rule"), marker)
	}
	return ExitOK
}

func runShow(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	refs, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(refs) != 1 {
		fmt.Fprintln(e.Stderr, "harness contract show: name exactly one contract, like docs or docs@2")
		return ExitUsage
	}
	id, version, err := parseRef(refs[0])
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness contract show: %v\n", err)
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	published, latest, code := lookupVersion(e, cmd, s, id, version)
	if code != ExitOK {
		return code
	}
	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema      int              `json:"schema"`
			Version     int              `json:"version"`
			Latest      bool             `json:"latest"`
			PublishedAt time.Time        `json:"publishedAt"`
			Contract    backend.Contract `json:"contract"`
		}{JSONSchema, published.Version, latest, published.PublishedAt, published.Contract})
		return ExitOK
	}
	data, err := contractfile.Marshal(published.Contract)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness contract show: %v\n", err)
		return ExitOperational
	}
	fmt.Fprintf(e.Stdout, "# %s@%d, published %s\n", id, published.Version, published.PublishedAt.Local().Format("2006-01-02 15:04"))
	e.Stdout.Write(data)
	return ExitOK
}

// parseRef splits "docs@2" into ("docs", 2). "docs" and "docs@latest" mean
// the latest version, returned as 0.
func parseRef(ref string) (string, int, error) {
	id, version, hasVersion := strings.Cut(ref, "@")
	if id == "" {
		return "", 0, fmt.Errorf("%q: missing contract ID", ref)
	}
	if !hasVersion || version == "latest" {
		return id, 0, nil
	}
	n, err := strconv.Atoi(version)
	if err != nil || n < 1 {
		return "", 0, fmt.Errorf("%q: version must be a positive number or \"latest\"", ref)
	}
	return id, n, nil
}

// lookupVersion returns a published version (0 = latest) and whether it is
// the latest, reporting unknown contracts and versions as usage errors.
func lookupVersion(e Env, cmd *command, s *session, id string, version int) (backend.PublishedContract, bool, int) {
	versions, err := s.service.ListVersions(id)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return backend.PublishedContract{}, false, ExitOperational
	}
	if len(versions) == 0 {
		fmt.Fprintf(e.Stderr, "harness %s: no published contract %q\n", cmd.name, id)
		return backend.PublishedContract{}, false, ExitUsage
	}
	if version == 0 {
		return versions[len(versions)-1], true, ExitOK
	}
	published, err := s.service.GetVersion(id, version)
	if errors.Is(err, backend.ErrNotFound) {
		fmt.Fprintf(e.Stderr, "harness %s: %s has no version %d (latest is %d)\n", cmd.name, id, version, len(versions))
		return backend.PublishedContract{}, false, ExitUsage
	}
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return backend.PublishedContract{}, false, ExitOperational
	}
	return published, version == len(versions), ExitOK
}
