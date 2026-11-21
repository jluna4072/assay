package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"assay/backend"
	"assay/contractfile"
)

var graphCommand = command{
	name:    "contract graph",
	args:    "<id>[@version] | <file>",
	summary: "Draw a contract's rule dependencies",
	details: `
Draws the dependency graph of a published version, or of a contract file
before it is published. Each rule is listed with the rules it depends on
beneath it, starting from the rules nothing else depends on. A rule reached
twice is drawn once and then marked "(see above)".

An argument containing "/" or ending in .yaml or .yml is read as a file;
anything else names a published contract.

Flags:
  --format <f>      text (default), mermaid, or dot
  --run <run>       Mark each rule with its result in the run's latest attempt
                    (a published contract only; any unique run ID prefix)
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Paste mermaid output into Markdown that renders Mermaid (GitHub does), or
render dot output with Graphviz: assay contract graph docs --format dot | dot -Tsvg
`,
	run: runGraph,
}

// ruleGraph is a contract's rules with lookups for drawing.
type ruleGraph struct {
	contract backend.Contract
	rules    map[string]backend.Rule
	roots    []string
	results  map[string]backend.RuleResult // optional run overlay
}

func newRuleGraph(contract backend.Contract) ruleGraph {
	g := ruleGraph{contract: contract, rules: map[string]backend.Rule{}}
	dependedOn := map[string]bool{}
	for _, rule := range contract.Rules {
		g.rules[rule.ID] = rule
		for _, dependency := range rule.DependsOn {
			dependedOn[dependency] = true
		}
	}
	for _, rule := range contract.Rules {
		if !dependedOn[rule.ID] {
			g.roots = append(g.roots, rule.ID)
		}
	}
	return g
}

type graphJSON struct {
	Schema   int             `json:"schema"`
	Contract string          `json:"contract"`
	Name     string          `json:"name"`
	Version  int             `json:"version,omitempty"`
	File     string          `json:"file,omitempty"`
	Run      *graphRunJSON   `json:"run,omitempty"`
	Roots    []string        `json:"roots"`
	Rules    []graphRuleJSON `json:"rules"`
}

type graphRunJSON struct {
	ID      string `json:"id"`
	Attempt int    `json:"attempt"`
}

type graphRuleJSON struct {
	ID        string   `json:"id"`
	Criterion string   `json:"criterion"`
	DependsOn []string `json:"dependsOn"`
	Status    string   `json:"status,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

func runGraph(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	format := fs.String("format", "text", "")
	runID := fs.String("run", "", "")
	projectDir := fs.String("project", "", "")
	positional, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(positional) != 1 {
		fmt.Fprintln(e.Stderr, "assay contract graph: name exactly one contract (docs, docs@2) or contract file")
		return ExitUsage
	}
	switch *format {
	case "text", "mermaid", "dot":
	default:
		fmt.Fprintf(e.Stderr, "assay contract graph: unknown format %q (use text, mermaid, or dot)\n", *format)
		return ExitUsage
	}
	if *asJSON && *format != "text" {
		fmt.Fprintln(e.Stderr, "assay contract graph: --json and --format can't be combined")
		return ExitUsage
	}
	arg := positional[0]
	out := graphJSON{Schema: JSONSchema}
	var title string
	var g ruleGraph

	if isContractFile(arg) {
		if *runID != "" {
			fmt.Fprintln(e.Stderr, "assay contract graph: --run needs a published contract, not a file")
			return ExitUsage
		}
		result, err := contractfile.Check(resolve(e, arg))
		if err != nil {
			fmt.Fprintf(e.Stderr, "assay contract graph: %v\n", err)
			return ExitOperational
		}
		if !result.Valid() {
			printProblems(e, arg, result.Problems)
			return ExitNegative
		}
		g = newRuleGraph(result.Contract)
		out.File = arg
		title = fmt.Sprintf("%s (%s, unpublished)", arg, result.Contract.Name)
	} else {
		id, version, err := parseRef(arg)
		if err != nil {
			fmt.Fprintf(e.Stderr, "assay contract graph: %v\n", err)
			return ExitUsage
		}
		s, code := openProject(e, cmd, *projectDir)
		if s == nil {
			return code
		}
		defer s.Close()

		var run backend.Run
		if *runID != "" {
			if run, code = findRun(e, cmd, s, *runID); code != ExitOK {
				return code
			}
			if run.ContractID != id {
				fmt.Fprintf(e.Stderr, "assay contract graph: run %s checks %s@%d, not %s\n", shortID(run.ID), run.ContractID, run.Version, id)
				return ExitUsage
			}
			if version != 0 && version != run.Version {
				fmt.Fprintf(e.Stderr, "assay contract graph: run %s checks %s@%d, not %s@%d\n", shortID(run.ID), id, run.Version, id, version)
				return ExitUsage
			}
			version = run.Version
		}
		published, _, code := lookupVersion(e, cmd, s, id, version)
		if code != ExitOK {
			return code
		}
		g = newRuleGraph(published.Contract)
		out.Version = published.Version
		title = fmt.Sprintf("%s@%d (%s)", id, published.Version, published.Contract.Name)
		if *runID != "" {
			if len(run.Attempts) == 0 {
				fmt.Fprintf(e.Stderr, "assay contract graph: run %s has no attempts yet\n", shortID(run.ID))
				return ExitUsage
			}
			latest := run.Attempts[len(run.Attempts)-1]
			g.results = map[string]backend.RuleResult{}
			for _, result := range latest.Results {
				g.results[result.RuleID] = result
			}
			out.Run = &graphRunJSON{ID: strings.ToLower(run.ID), Attempt: latest.Number}
			title += fmt.Sprintf(" · run %s, attempt %d", shortID(run.ID), latest.Number)
		}
	}

	if *asJSON {
		out.Contract, out.Name, out.Roots = g.contract.ID, g.contract.Name, g.roots
		for _, rule := range g.contract.Rules {
			entry := graphRuleJSON{ID: rule.ID, Criterion: rule.Criterion, DependsOn: rule.DependsOn}
			if entry.DependsOn == nil {
				entry.DependsOn = []string{}
			}
			if result, ok := g.results[rule.ID]; ok {
				entry.Status, entry.Reason = string(result.Status), result.Reason
			}
			out.Rules = append(out.Rules, entry)
		}
		writeJSON(e.Stdout, out)
		return ExitOK
	}
	switch *format {
	case "mermaid":
		writeMermaid(e.Stdout, g)
	case "dot":
		writeDot(e.Stdout, g, title)
	default:
		fmt.Fprintln(e.Stdout, title)
		writeTree(e.Stdout, g)
	}
	return ExitOK
}

func isContractFile(arg string) bool {
	return strings.ContainsRune(arg, '/') || strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml")
}

// writeTree draws each root with its dependencies beneath it.
func writeTree(w io.Writer, g ruleGraph) {
	drawn := map[string]bool{}
	var draw func(id, prefix, branch, childPrefix string)
	draw = func(id, prefix, branch, childPrefix string) {
		rule := g.rules[id]
		label := id
		if result, ok := g.results[id]; ok {
			label = statusMarks[result.Status] + " " + id
		}
		if drawn[id] && len(rule.DependsOn) > 0 {
			fmt.Fprintf(w, "%s%s%s (see above)\n", prefix, branch, label)
			return
		}
		drawn[id] = true
		detail := rule.Criterion
		if result, ok := g.results[id]; ok && result.Status != backend.Passed && result.Reason != "" {
			detail = result.Reason
			if result.Status == backend.Blocked {
				detail = "blocked: " + detail
			}
		}
		fmt.Fprintf(w, "%s%s%s — %s\n", prefix, branch, label, detail)
		for i, dependency := range rule.DependsOn {
			if i == len(rule.DependsOn)-1 {
				draw(dependency, prefix+childPrefix, "└── ", "    ")
			} else {
				draw(dependency, prefix+childPrefix, "├── ", "│   ")
			}
		}
	}
	for _, root := range g.roots {
		draw(root, "", "", "")
	}
}

var statusColors = map[backend.ResultStatus][2]string{ // fill, stroke
	backend.Passed:           {"#d3f9d8", "#2b8a3e"},
	backend.Failed:           {"#ffe3e3", "#c92a2a"},
	backend.Blocked:          {"#f1f3f5", "#868e96"},
	backend.OperationalError: {"#fff3bf", "#e67700"},
}

var statusOrder = []backend.ResultStatus{backend.Passed, backend.Failed, backend.Blocked, backend.OperationalError}

// writeMermaid draws a flowchart with an arrow from each rule to each rule
// it depends on. Node names are positional so any rule ID is safe.
func writeMermaid(w io.Writer, g ruleGraph) {
	node := map[string]string{}
	for i, rule := range g.contract.Rules {
		node[rule.ID] = fmt.Sprintf("r%d", i+1)
	}
	fmt.Fprintln(w, "flowchart TD")
	for _, rule := range g.contract.Rules {
		label := rule.ID + ": " + rule.Criterion
		if result, ok := g.results[rule.ID]; ok {
			label = statusMarks[result.Status] + " " + label
		}
		fmt.Fprintf(w, "  %s[\"%s\"]\n", node[rule.ID], mermaidEscape(label))
	}
	for _, rule := range g.contract.Rules {
		for _, dependency := range rule.DependsOn {
			fmt.Fprintf(w, "  %s -->|depends on| %s\n", node[rule.ID], node[dependency])
		}
	}
	if g.results == nil {
		return
	}
	for _, status := range statusOrder {
		var members []string
		for _, rule := range g.contract.Rules {
			if g.results[rule.ID].Status == status {
				members = append(members, node[rule.ID])
			}
		}
		if len(members) == 0 {
			continue
		}
		colors := statusColors[status]
		fmt.Fprintf(w, "  classDef %s fill:%s,stroke:%s\n", status, colors[0], colors[1])
		fmt.Fprintf(w, "  class %s %s\n", strings.Join(members, ","), status)
	}
}

func mermaidEscape(s string) string {
	return strings.NewReplacer(`"`, "#quot;", "\n", " ").Replace(s)
}

// writeDot draws a Graphviz digraph with an edge from each rule to each rule
// it depends on.
func writeDot(w io.Writer, g ruleGraph, title string) {
	fmt.Fprintf(w, "digraph %s {\n", dotQuote(title))
	fmt.Fprintln(w, "  rankdir=TB;")
	fmt.Fprintln(w, `  node [shape=box, style="rounded,filled", fillcolor="#ffffff", fontname="Helvetica"];`)
	for _, rule := range g.contract.Rules {
		attributes := fmt.Sprintf("label=%s", dotQuote(rule.ID+"\n"+rule.Criterion))
		if result, ok := g.results[rule.ID]; ok {
			colors := statusColors[result.Status]
			attributes += fmt.Sprintf(`, fillcolor="%s", color="%s", tooltip=%s`, colors[0], colors[1], dotQuote(string(result.Status)+": "+result.Reason))
		}
		fmt.Fprintf(w, "  %s [%s];\n", dotQuote(rule.ID), attributes)
	}
	for _, rule := range g.contract.Rules {
		for _, dependency := range rule.DependsOn {
			fmt.Fprintf(w, "  %s -> %s;\n", dotQuote(rule.ID), dotQuote(dependency))
		}
	}
	fmt.Fprintln(w, "}")
}

func dotQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
