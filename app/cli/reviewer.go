package cli

import (
	"flag"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"assay/backend"
	"assay/project"
)

var reviewerListCommand = command{
	name:    "reviewer list",
	args:    "",
	summary: "List the reviewers configured for this project",
	details: `
Lists the reviewers in .assay/config.yaml: what each one runs, and which
one reviews rules that don't name a reviewer.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 listed, 2 the config can't be read, 64 usage error.
`,
	run: runReviewerList,
}

var reviewerTestCommand = command{
	name:    "reviewer test",
	args:    "[name]",
	summary: "Check that a reviewer works by sending it two sample reviews",
	details: `
Sends a reviewer (the default reviewer without a name) two fixed sample
reviews: one whose evidence meets the criterion and one whose evidence
doesn't. A working reviewer passes the first and fails the second. Nothing
is recorded in the project.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 both samples judged as expected, 1 a sample was judged wrongly,
2 the reviewer couldn't be run or gave an unusable answer, 64 usage error.
`,
	run: runReviewerTest,
}

type reviewerJSON struct {
	Name      string   `json:"name"`
	Protocol  string   `json:"protocol"`
	Default   bool     `json:"default"`
	Run       []string `json:"run,omitempty"`
	Network   string   `json:"network,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Model     string   `json:"model,omitempty"`
	Threshold float64  `json:"threshold,omitempty"`
	Key       string   `json:"key,omitempty"` // where its API key comes from: $VAR, keychain, not needed, or missing
	Timeout   string   `json:"timeout"`
}

func runReviewerList(e Env, cmd *command, args []string) int {
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
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()
	config, err := project.LoadConfig(s.project.Root)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return ExitOperational
	}

	defaultSpec, _ := config.Reviewer("")
	list := []reviewerJSON{}
	for _, spec := range config.Reviewers {
		item := reviewerJSON{Name: spec.Name, Protocol: string(spec.Protocol), Default: spec.Name == defaultSpec.Name}
		if spec.Protocol == backend.CommandProtocol {
			timeout := spec.Command.Timeout
			if timeout <= 0 {
				timeout = backend.DefaultReviewTimeout
			}
			item.Run, item.Network, item.Timeout = spec.Command.Run, string(spec.Command.Network), timeout.String()
		}
		if backend.UsesAPIKey(spec.Protocol) {
			d := spec.Settings()
			item.Endpoint, item.Model, item.Timeout = d.Endpoint, d.Model, d.Timeout.String()
			if backend.DecisionModel(spec.Protocol) {
				item.Threshold = d.Threshold
			}
			item.Key = keySource(spec)
		}
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema    int            `json:"schema"`
			Reviewers []reviewerJSON `json:"reviewers"`
		}{JSONSchema, list})
		return ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintf(e.Stdout, "no reviewers configured. Add one under reviewers: in %s.\n", s.project.ConfigPath())
		return ExitOK
	}
	table := tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tPROTOCOL\tUSES\tKEY\tTIMEOUT")
	for _, r := range list {
		name := r.Name
		if r.Default {
			name += " (default)"
		}
		uses, key := strings.Join(r.Run, " "), "-"
		if backend.UsesAPIKey(backend.ReviewProtocol(r.Protocol)) {
			uses, key = r.Model, r.Key
			if r.Threshold > 0 {
				uses = fmt.Sprintf("%s ≥ %.2f", r.Model, r.Threshold)
			}
		} else if r.Network != "" {
			uses += " (network " + r.Network + ")"
		}
		if len([]rune(uses)) > 48 {
			uses = string([]rune(uses)[:47]) + "…"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", name, r.Protocol, uses, key, r.Timeout)
	}
	table.Flush()
	return ExitOK
}

type reviewSampleJSON struct {
	Name     string     `json:"name"`
	WantPass bool       `json:"wantPass"`
	OK       bool       `json:"ok"`
	Review   reviewJSON `json:"review"`
}

func runReviewerTest(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	names, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(names) > 1 {
		fmt.Fprintf(e.Stderr, "assay %s: name at most one reviewer\n", cmd.name)
		return ExitUsage
	}
	name := ""
	if len(names) == 1 {
		name = names[0]
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	if *asJSON {
		s.service.Keys = newKeys(e, false)
	}
	spec, samples, err := s.service.TestReviewer(s.project.Root, name)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return ExitOperational
	}
	exit := ExitOK
	for _, sample := range samples {
		switch {
		case sample.Review.Error != "":
			exit = ExitOperational
		case !sample.OK() && exit == ExitOK:
			exit = ExitNegative
		}
	}

	if *asJSON {
		list := make([]reviewSampleJSON, len(samples))
		for i, sample := range samples {
			list[i] = reviewSampleJSON{Name: sample.Name, WantPass: sample.WantPass, OK: sample.OK(), Review: toReviewJSON(sample.Review)}
		}
		writeJSON(e.Stdout, struct {
			Schema   int                `json:"schema"`
			Reviewer string             `json:"reviewer"`
			OK       bool               `json:"ok"`
			Samples  []reviewSampleJSON `json:"samples"`
		}{JSONSchema, spec.Name, exit == ExitOK, list})
		return exit
	}

	fmt.Fprintf(e.Stdout, "%s · %s protocol · %s\n\n", spec.Name, spec.Protocol, spec.Describe())
	for _, sample := range samples {
		mark := "✓"
		if !sample.OK() {
			mark = "✗"
		}
		want := "fail"
		if sample.WantPass {
			want = "pass"
		}
		fmt.Fprintf(e.Stdout, "  %s sample that %s (should %s)\n", mark, sample.Name, want)
		line := describeReview(sample.Review)
		if sample.Review.Explanation != "" {
			line += ": " + oneLine(sample.Review.Explanation)
		}
		fmt.Fprintf(e.Stdout, "      %s\n", line)
	}
	fmt.Fprintln(e.Stdout)
	switch exit {
	case ExitOK:
		fmt.Fprintf(e.Stdout, "%s works: it passed the sample that meets the criterion and failed the one that doesn't.\n", spec.Name)
	case ExitNegative:
		fmt.Fprintf(e.Stdout, "%s answered, but judged a sample wrongly. Check its model and prompt before relying on it.\n", spec.Name)
	default:
		fmt.Fprintf(e.Stdout, "%s couldn't review the samples. Fix the error above, then run \"assay reviewer test %s\" again.\n", spec.Name, spec.Name)
	}
	return exit
}

// describeReview summarizes a review on one line, without its explanation.
func describeReview(review backend.Review) string {
	if review.Error != "" {
		return "error: " + oneLine(review.Error)
	}
	parts := []string{string(review.Verdict)}
	if review.Probability != nil {
		parts = append(parts, fmt.Sprintf("p=%.2f", *review.Probability))
	}
	if review.Model != "" {
		parts = append(parts, "model "+review.Model)
	}
	if len(review.Cites) > 0 {
		cites := make([]string, len(review.Cites))
		for i, c := range review.Cites {
			cites[i] = fmt.Sprint(c)
		}
		parts = append(parts, "cites "+strings.Join(cites, ", "))
	}
	parts = append(parts, formatDuration(review.Duration))
	return strings.Join(parts, " · ")
}

// oneLine collapses whitespace, including newlines, to single spaces.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

type reviewJSON struct {
	Reviewer      string   `json:"reviewer"`
	Protocol      string   `json:"protocol,omitempty"`
	Model         string   `json:"model,omitempty"`
	Verdict       string   `json:"verdict,omitempty"`
	Probability   *float64 `json:"probability,omitempty"`
	Explanation   string   `json:"explanation,omitempty"`
	Cites         []int    `json:"cites,omitempty"`
	RequestSHA256 string   `json:"requestSha256,omitempty"`
	Duration      int64    `json:"durationMs"`
	Sandboxed     bool     `json:"sandboxed"`
	Error         string   `json:"error,omitempty"`
}

func toReviewJSON(review backend.Review) reviewJSON {
	return reviewJSON{
		Reviewer: review.Reviewer, Protocol: string(review.Protocol), Model: review.Model, Verdict: string(review.Verdict),
		Probability: review.Probability, Explanation: review.Explanation, Cites: review.Cites, RequestSHA256: review.RequestHash,
		Duration: review.Duration, Sandboxed: review.Sandboxed, Error: review.Error,
	}
}
