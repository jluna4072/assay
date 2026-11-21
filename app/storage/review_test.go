package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/backend"
)

// reviewerConfig configures command reviewers that run shell scripts.
func reviewerConfig(sandbox bool, scripts map[string]string) backend.Config {
	config := backend.DefaultConfig()
	config.Sandbox = sandbox
	for name, script := range scripts {
		config.Reviewers[name] = backend.ReviewerSpec{Name: name, Protocol: backend.CommandProtocol, Command: backend.CommandSpec{Run: sh(script)}}
	}
	return config
}

func reviewRule(id, reviewer string, evidence ...backend.EvidenceSpec) backend.Rule {
	if len(evidence) == 0 {
		evidence = []backend.EvidenceSpec{{Kind: backend.FileEvidence, Path: "README.md"}}
	}
	return backend.Rule{ID: id, Criterion: "README greets the reader", Kind: backend.ReviewCheck, Reviewer: reviewer, Evidence: evidence}
}

// respond is a reviewer script that prints a fixed response.
func respond(response string) string {
	return "cat >/dev/null; printf '%s' '" + strings.ReplaceAll(response, "'", `'\''`) + "'"
}

func TestReviewVerdicts(t *testing.T) {
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello, reader\n"), 0600)
	run := commandRun(t, reviewerConfig(false, map[string]string{
		// Judges the evidence it's sent, not the criterion.
		"reads":   `if grep -q '"content":"Hello'; then echo '{"protocol":1,"verdict":"pass","explanation":"It says hello.","cites":[1],"model":"m-1","probability":0.97}'; else echo '{"protocol":1,"verdict":"fail"}'; fi`,
		"fails":   respond(`{"protocol":1,"verdict":"fail","explanation":"No greeting.\nAt all."}`),
		"unsure":  respond(`{"protocol":1,"verdict":"insufficient_evidence","explanation":"Can't tell."}`),
		"silent":  respond(`{"protocol":1,"verdict":"pass"}`),
		"unknown": respond(`{"protocol":1,"verdict":"pass","extra":"ignored"}`),
	}), project,
		reviewRule("reads", "reads"), reviewRule("fails", "fails"), reviewRule("unsure", "unsure"),
		reviewRule("silent", "silent"), reviewRule("extra-fields", "unknown"))
	results := run.Attempts[0].Results

	reads := results[0]
	if reads.Status != backend.Passed || reads.Reason != "reads: It says hello." {
		t.Fatalf("reads: %+v", reads)
	}
	review := reads.Review
	if review == nil || review.Verdict != backend.VerdictPass || review.Model != "m-1" || *review.Probability != 0.97 || len(review.Cites) != 1 || review.Sandboxed || review.RequestHash == "" {
		t.Fatalf("review: %+v", review)
	}
	if len(reads.Evidence) != 3 || reads.Evidence[1].Kind != backend.ReviewRequestEvidence || reads.Evidence[2].Kind != backend.ReviewResponseEvidence || reads.Evidence[1].Reviewer != "reads" {
		t.Fatalf("evidence: %+v", reads.Evidence)
	}
	var request backend.ReviewRequest
	if err := json.Unmarshal(mustBlob(t, run, reads.Evidence[1]), &request); err != nil {
		t.Fatal(err)
	}
	if request.Protocol != 1 || request.Rule.Criterion != "README greets the reader" || len(request.Evidence) != 1 ||
		request.Evidence[0].Item != 1 || request.Evidence[0].Content != "Hello, reader\n" || request.Evidence[0].SHA256 != reads.Evidence[0].SHA256 {
		t.Fatalf("request: %+v", request)
	}
	if reads.Evidence[1].SHA256 != review.RequestHash {
		t.Fatal("request hash doesn't match the stored request")
	}
	if got := string(mustBlob(t, run, reads.Evidence[2])); !strings.Contains(got, `"model":"m-1"`) {
		t.Fatalf("response stored as %q", got)
	}

	for i, want := range []struct {
		status backend.ResultStatus
		reason string
	}{
		{backend.Failed, "fails: No greeting.\nAt all."},
		{backend.Failed, "unsure: Can't tell. (insufficient evidence)"},
		{backend.Passed, "silent: criterion met"},
		{backend.Passed, "unknown: criterion met"},
	} {
		if got := results[i+1]; got.Status != want.status || got.Reason != want.reason {
			t.Errorf("%s: %s %q, want %s %q", got.RuleID, got.Status, got.Reason, want.status, want.reason)
		}
	}
}

// No failure to get a usable verdict may pass a rule.
func TestReviewFailuresAreErrors(t *testing.T) {
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello\n"), 0600)
	os.WriteFile(filepath.Join(project, "big.txt"), []byte(strings.Repeat("x", backend.MaxEvidenceBytes+1)), 0600)
	scripts := map[string]string{
		"crashes":     "cat >/dev/null; echo 'model overloaded' >&2; exit 3",
		"garbage":     respond(`I think it passes.`),
		"refuses":     respond(`I can't help with that.`),
		"empty":       respond(``),
		"two":         respond(`{"protocol":1,"verdict":"pass"} {"protocol":1,"verdict":"pass"}`),
		"noprotocol":  respond(`{"verdict":"pass"}`),
		"protocol2":   respond(`{"protocol":2,"verdict":"pass"}`),
		"noverdict":   respond(`{"protocol":1,"explanation":"looks fine"}`),
		"shouting":    respond(`{"protocol":1,"verdict":"PASS"}`),
		"yes":         respond(`{"protocol":1,"verdict":"yes"}`),
		"badcite":     respond(`{"protocol":1,"verdict":"pass","cites":[2]}`),
		"zerocite":    respond(`{"protocol":1,"verdict":"pass","cites":[0]}`),
		"probability": respond(`{"protocol":1,"verdict":"pass","probability":1.5}`),
		"wrongtype":   respond(`{"protocol":1,"verdict":true}`),
		"huge":        "cat >/dev/null; head -c 2000000 /dev/zero | tr '\\0' ' '; echo '{\"protocol\":1,\"verdict\":\"pass\"}'",
		"passes":      respond(`{"protocol":1,"verdict":"pass"}`),
	}
	config := reviewerConfig(false, scripts)
	slow := config.Reviewers["passes"]
	slow.Name, slow.Command.Run, slow.Command.Timeout = "slow", sh("sleep 5; "+respond(`{"protocol":1,"verdict":"pass"}`)), 200*time.Millisecond
	config.Reviewers["slow"] = slow
	config.Reviewers["future"] = backend.ReviewerSpec{Name: "future", Protocol: "openai"}

	rules := []backend.Rule{}
	for name := range scripts {
		if name != "passes" {
			rules = append(rules, reviewRule(name, name))
		}
	}
	rules = append(rules,
		reviewRule("slow", "slow"),
		reviewRule("future", "future"),
		reviewRule("unconfigured", "nobody"),
		reviewRule("no-default", ""), // several reviewers and no default_reviewer
		reviewRule("missing-evidence", "passes", backend.EvidenceSpec{Kind: backend.DiffEvidence}), // not a git repository
		reviewRule("too-big", "passes", backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "big.txt"}),
	)
	started := time.Now()
	run := commandRun(t, config, project, rules...)
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("the slow reviewer wasn't stopped at its timeout (took %s)", elapsed)
	}

	want := map[string]string{
		"crashes":          "reviewer crashes exited with status 3: model overloaded",
		"garbage":          "expected one JSON object",
		"refuses":          "expected one JSON object",
		"empty":            "expected one JSON object",
		"two":              "found more after it",
		"noprotocol":       `"protocol" must be 1`,
		"protocol2":        `"protocol" must be 1`,
		"noverdict":        `"verdict" is missing`,
		"shouting":         `unknown verdict "PASS"`,
		"yes":              `unknown verdict "yes"`,
		"badcite":          "cites evidence item 2, but the request had 1 item",
		"zerocite":         "cites evidence item 0",
		"probability":      `"probability" must be between 0 and 1`,
		"wrongtype":        "expected one JSON object",
		"huge":             "wrote more than 1048576 bytes",
		"slow":             "reviewer slow timed out after 200ms",
		"future":           `uses protocol "openai", which this harness doesn't support`,
		"unconfigured":     `reviewer "nobody" isn't configured`,
		"no-default":       "sets no default_reviewer",
		"missing-evidence": "couldn't be collected, so it can't be reviewed",
		"too-big":          "larger than 1048576 bytes",
	}
	for _, result := range run.Attempts[0].Results {
		if result.Status != backend.OperationalError || !strings.Contains(result.Reason, want[result.RuleID]) {
			t.Errorf("%s: %s %q, want error containing %q", result.RuleID, result.Status, result.Reason, want[result.RuleID])
		}
	}
	for _, result := range run.Attempts[0].Results {
		switch result.RuleID {
		case "missing-evidence", "too-big", "unconfigured", "no-default", "future":
			// Never sent: no request recorded.
			for _, item := range result.Evidence {
				if item.Kind == backend.ReviewRequestEvidence {
					t.Errorf("%s: sent a request it shouldn't have", result.RuleID)
				}
			}
		case "garbage":
			// The unusable response is kept for diagnosis.
			if n := len(result.Evidence); n != 3 || string(mustBlob(t, run, result.Evidence[2])) != "I think it passes." {
				t.Errorf("garbage response not recorded: %+v", result.Evidence)
			}
		}
	}
	if run.Attempts[0].Status != backend.OperationalError {
		t.Fatalf("attempt status %s", run.Attempts[0].Status)
	}
}

func TestReviewerDefaultAndAbsentEvidence(t *testing.T) {
	project := t.TempDir()
	config := reviewerConfig(false, map[string]string{
		"only": `if grep -q '"absent":true'; then echo '{"protocol":1,"verdict":"fail","explanation":"There is no README."}'; else echo '{"protocol":1,"verdict":"pass"}'; fi`,
	})
	// The only reviewer is the default; an absent file is still evidence.
	run := commandRun(t, config, project, reviewRule("readme", ""))
	result := run.Attempts[0].Results[0]
	if result.Status != backend.Failed || result.Reason != "only: There is no README." || result.Review.Reviewer != "only" {
		t.Fatalf("result: %+v", result)
	}
}

func TestSandboxLimitsReviewers(t *testing.T) {
	requireSandbox(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello\n"), 0600)
	run := commandRun(t, reviewerConfig(true, map[string]string{
		"sneaky": `cat >/dev/null; touch "$PWD/written-by-reviewer" 2>/dev/null; echo '{"protocol":1,"verdict":"pass"}'`,
	}), project, reviewRule("readme", "sneaky"))
	result := run.Attempts[0].Results[0]
	if result.Status != backend.Passed || !result.Review.Sandboxed {
		t.Fatalf("result: %+v %+v", result, result.Review)
	}
	if _, err := os.Stat(filepath.Join(project, "written-by-reviewer")); err == nil {
		t.Fatal("a reviewer wrote to the project")
	}
}

func TestReviewerSelfTest(t *testing.T) {
	_, s := openTest(t)
	s.LoadConfig = func(string) (backend.Config, error) {
		return reviewerConfig(false, map[string]string{
			"good":  `if grep -q '"content":"Hello'; then echo '{"protocol":1,"verdict":"pass"}'; else echo '{"protocol":1,"verdict":"fail"}'; fi`,
			"yes":   respond(`{"protocol":1,"verdict":"pass"}`),
			"broke": "exit 1",
		}), nil
	}
	for name, want := range map[string][2]bool{"good": {true, true}, "yes": {true, false}, "broke": {false, false}} {
		spec, samples, err := s.TestReviewer(t.TempDir(), name)
		if err != nil || spec.Name != name || len(samples) != 2 {
			t.Fatalf("%s: %v %+v", name, err, samples)
		}
		if samples[0].OK() != want[0] || samples[1].OK() != want[1] {
			t.Errorf("%s: samples %+v", name, samples)
		}
	}
	if _, _, err := s.TestReviewer(t.TempDir(), "missing"); err == nil || !strings.Contains(err.Error(), `reviewer "missing" isn't configured`) {
		t.Fatalf("missing reviewer: %v", err)
	}
}
