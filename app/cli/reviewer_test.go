package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fake reviewer passes evidence that contains "Hello" and fails the
// rest, judging only the evidence it's sent.
const reviewerConfig = `version: 1
sandbox: off
reviewers:
  fake:
    protocol: command
    run: [sh, -c, 'if grep -q "\"content\":\"Hello"; then echo "{\"protocol\":1,\"verdict\":\"pass\",\"explanation\":\"It greets.\",\"cites\":[1],\"model\":\"fake-1\"}"; else echo "{\"protocol\":1,\"verdict\":\"fail\",\"explanation\":\"No greeting.\"}"; fi']
  yes:
    protocol: command
    run: [sh, -c, 'cat >/dev/null; echo "{\"protocol\":1,\"verdict\":\"pass\"}"']
  broken:
    protocol: command
    run: [sh, -c, 'echo "out of credits" >&2; exit 1']
    timeout: 30s
default_reviewer: fake
`

const reviewContract = `version: 1
id: greet
name: Greeting
rules:
  - id: says-hello
    criterion: README greets the reader
    check: review
    evidence:
      - file: README.md
`

func reviewerProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "init")
	os.WriteFile(filepath.Join(dir, ".assay", "config.yaml"), []byte(reviewerConfig), 0600)
	os.WriteFile(filepath.Join(dir, "assay", "greet.yaml"), []byte(reviewContract), 0644)
	return dir
}

func TestReviewerList(t *testing.T) {
	dir := reviewerProject(t)
	out := mustRun(t, dir, "reviewer", "list")
	for _, want := range []string{"NAME", "broken", "fake (default)  command", "yes", "30s", "2m0s"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	var listed struct {
		Reviewers []reviewerJSON `json:"reviewers"`
	}
	if err := json.Unmarshal([]byte(mustRun(t, dir, "reviewer", "list", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Reviewers) != 3 || listed.Reviewers[1].Name != "fake" || !listed.Reviewers[1].Default || listed.Reviewers[0].Default {
		t.Fatalf("json: %+v", listed)
	}

	empty := t.TempDir()
	mustRun(t, empty, "init")
	if out := mustRun(t, empty, "reviewer", "list"); !strings.Contains(out, "no reviewers configured") {
		t.Fatalf("empty: %s", out)
	}
}

func TestReviewerTest(t *testing.T) {
	dir := reviewerProject(t)
	out := mustRun(t, dir, "reviewer", "test")
	for _, want := range []string{
		"fake · command protocol · sh -c",
		"✓ sample that meets the criterion (should pass)\n      pass · model fake-1 · cites 1 · ",
		"✓ sample that doesn't meet the criterion (should fail)\n      fail · ",
		": No greeting.\n",
		"fake works",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("test lacks %q:\n%s", want, out)
		}
	}
	if out, _, code := runIn(dir, false, "reviewer", "test", "yes"); code != ExitNegative || !strings.Contains(out, "✗ sample that doesn't meet the criterion") {
		t.Fatalf("yes: exit %d\n%s", code, out)
	}
	if out, _, code := runIn(dir, false, "reviewer", "test", "broken"); code != ExitOperational || !strings.Contains(out, "error: reviewer broken exited with status 1: out of credits") {
		t.Fatalf("broken: exit %d\n%s", code, out)
	}
	if _, stderr, code := runIn(dir, false, "reviewer", "test", "nobody"); code != ExitOperational || !strings.Contains(stderr, `reviewer "nobody" isn't configured`) {
		t.Fatalf("nobody: exit %d\n%s", code, stderr)
	}
	stdout, _, code := runIn(dir, false, "reviewer", "test", "yes", "--json")
	var result struct {
		OK      bool               `json:"ok"`
		Samples []reviewSampleJSON `json:"samples"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || code != ExitNegative || result.OK || !result.Samples[0].OK || result.Samples[1].OK || result.Samples[1].Review.Verdict != "pass" {
		t.Fatalf("json: exit %d %v\n%s", code, err, stdout)
	}
}

func TestReviewRuleCheck(t *testing.T) {
	dir := reviewerProject(t)
	mustRun(t, dir, "contract", "publish", "assay/greet.yaml", "--yes")

	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Goodbye\n"), 0644)
	out, _, code := runIn(dir, false, "check", "greet")
	if code != ExitNegative || !strings.Contains(out, "✗ says-hello   fake: No greeting.\n") {
		t.Fatalf("check: exit %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Hello, reader\n"), 0644)
	if out := mustRun(t, dir, "recheck"); !strings.Contains(out, "✓ says-hello   fake: It greets.") {
		t.Fatalf("recheck:\n%s", out)
	}
	show := mustRun(t, dir, "show", "--evidence", "--attempt", "2")
	for _, want := range []string{
		"      review by fake · pass · model fake-1 · cites 1 · ",
		"      [1] file README.md · 14 B · ",
		"      [2] review request to fake · ",
		"      [3] review response from fake · ",
	} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	request := mustRun(t, dir, "evidence", "says-hello", "--item", "2")
	if !strings.Contains(request, `"criterion":"README greets the reader"`) || !strings.Contains(request, `"content":"Hello, reader\n"`) {
		t.Fatalf("request: %s", request)
	}

	var shown struct {
		Run runJSON `json:"run"`
	}
	if err := json.Unmarshal([]byte(mustRun(t, dir, "show", "--json")), &shown); err != nil {
		t.Fatal(err)
	}
	review := shown.Run.Attempts[1].Results[0].Review
	if review == nil || review.Reviewer != "fake" || review.Verdict != "pass" || review.Model != "fake-1" || review.Explanation != "It greets." || review.RequestSHA256 == "" {
		t.Fatalf("json review: %+v", review)
	}
	if evidence := shown.Run.Attempts[1].Results[0].Evidence; len(evidence) != 3 || evidence[2].Kind != "review_response" || evidence[2].Reviewer != "fake" {
		t.Fatalf("json evidence: %+v", evidence)
	}
}

func TestUnconfiguredReviewerIsAProblem(t *testing.T) {
	dir := reviewerProject(t)
	contract := strings.Replace(reviewContract, "check: review", "check: review\n    reviewer: careful", 1)
	os.WriteFile(filepath.Join(dir, "assay", "greet.yaml"), []byte(contract), 0644)
	want := `assay/greet.yaml:8:5: rule "says-hello": reviewer "careful" isn't configured in .assay/config.yaml`
	if out, _, code := runIn(dir, false, "contract", "validate", "assay/greet.yaml"); code != ExitNegative || !strings.Contains(out, want) {
		t.Fatalf("validate: exit %d\n%s", code, out)
	}
	if out, stderr, code := runIn(dir, false, "contract", "publish", "assay/greet.yaml", "--yes"); code != ExitNegative || !strings.Contains(out+stderr, want) {
		t.Fatalf("publish: exit %d\n%s%s", code, out, stderr)
	}
}
