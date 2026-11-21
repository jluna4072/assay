package cli

import (
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

	"harness/backend"
)

var workspaceListCommand = command{
	name:    "workspace list",
	args:    "",
	summary: "List the workspaces of work runs",
	details: `
Lists the runs made by "harness work", newest first, with each run's
outcome, branch, and whether its workspace is still on disk.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `
`,
	run: runWorkspaceList,
}

var workspaceCleanCommand = command{
	name:    "workspace clean",
	args:    "[run]",
	summary: "Remove the workspaces of finished work runs",
	details: `
Removes the workspaces (git worktrees under .harness/workspaces) of work
runs that have finished, or of the one run named. Branches are kept: they
hold every attempt's commit, and a cleaned run can still be resumed, which
recreates its workspace from the branch.

A workspace with changes that aren't committed (edits made after the run,
say) is skipped unless --force is given. Runs still in progress are always
skipped.

Flags:
  --force           Remove workspaces even if they have changes that
                    aren't committed
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 done, 1 a workspace was skipped, 2 an error, 64 usage error.
`,
	run: runWorkspaceClean,
}

type workspaceJSON struct {
	Run       string `json:"run"`
	Contract  string `json:"contract"`
	Outcome   string `json:"outcome"`
	Branch    string `json:"branch"`
	Workspace string `json:"workspace"`
	Exists    bool   `json:"exists"`
	Removed   bool   `json:"removed,omitempty"`
	Skipped   string `json:"skipped,omitempty"`
}

func toWorkspaceJSON(status backend.WorkspaceStatus) workspaceJSON {
	run := status.Run
	return workspaceJSON{
		Run: strings.ToLower(run.ID), Contract: fmt.Sprintf("%s@%d", run.ContractID, run.Version), Outcome: string(run.Work.Outcome),
		Branch: run.Work.Branch, Workspace: run.Work.Workspace, Exists: status.Exists, Removed: status.Removed, Skipped: status.Skipped,
	}
}

func runWorkspaceList(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	rest, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(e.Stderr, "harness %s: unexpected argument %q\n", cmd.name, rest[0])
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()
	list, err := s.service.Workspaces()
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return ExitOperational
	}
	if *asJSON {
		out := []workspaceJSON{}
		for _, status := range list {
			out = append(out, toWorkspaceJSON(status))
		}
		writeJSON(e.Stdout, struct {
			Schema     int             `json:"schema"`
			Workspaces []workspaceJSON `json:"workspaces"`
		}{JSONSchema, out})
		return ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintln(e.Stdout, "no work runs yet. Start one with \"harness work <contract> --task ...\".")
		return ExitOK
	}
	table := tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUN\tCONTRACT\tOUTCOME\tBRANCH\tWORKSPACE")
	for _, status := range list {
		workspace := relative(s.project.Root, status.Run.Work.Workspace)
		if !status.Exists {
			workspace = "(removed)"
		}
		fmt.Fprintf(table, "%s\t%s@%d\t%s\t%s\t%s\n", shortID(status.Run.ID), status.Run.ContractID, status.Run.Version, status.Run.Work.Outcome, status.Run.Work.Branch, workspace)
	}
	table.Flush()
	return ExitOK
}

func runWorkspaceClean(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	force := fs.Bool("force", false, "")
	projectDir := fs.String("project", "", "")
	ids, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(ids) > 1 {
		fmt.Fprintf(e.Stderr, "harness %s: name at most one run\n", cmd.name)
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()
	runID := ""
	if len(ids) == 1 {
		run, code := findRun(e, cmd, s, ids[0])
		if code != ExitOK {
			return code
		}
		if run.Work == nil {
			fmt.Fprintf(e.Stderr, "harness %s: run %s wasn't made by \"harness work\", so it has no workspace\n", cmd.name, shortID(run.ID))
			return ExitUsage
		}
		runID = run.ID
	}
	results, err := s.service.CleanWorkspaces(runID, *force)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return ExitOperational
	}
	exit := ExitOK
	for _, status := range results {
		if status.Skipped != "" {
			exit = ExitNegative
		}
	}
	if *asJSON {
		out := []workspaceJSON{}
		for _, status := range results {
			out = append(out, toWorkspaceJSON(status))
		}
		writeJSON(e.Stdout, struct {
			Schema     int             `json:"schema"`
			Workspaces []workspaceJSON `json:"workspaces"`
		}{JSONSchema, out})
		return exit
	}
	if len(results) == 0 {
		fmt.Fprintln(e.Stdout, "no workspaces to remove")
		return ExitOK
	}
	for _, status := range results {
		if status.Removed {
			fmt.Fprintf(e.Stdout, "removed  %s (run %s, %s); branch %s kept\n", relative(s.project.Root, status.Run.Work.Workspace), shortID(status.Run.ID), status.Run.Work.Outcome, status.Run.Work.Branch)
		} else {
			fmt.Fprintf(e.Stdout, "skipped  %s (run %s): %s\n", relative(s.project.Root, status.Run.Work.Workspace), shortID(status.Run.ID), status.Skipped)
		}
	}
	return exit
}
