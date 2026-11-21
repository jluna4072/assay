package storage

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"assay/backend"
)

type staticKeys map[string]string

// Key returns the reviewer's key, or none for a local endpoint.
func (k staticKeys) Key(spec backend.ReviewerSpec) (string, error) {
	if key, ok := k[spec.Name]; ok {
		return key, nil
	}
	if spec.API.Local() {
		return "", nil
	}
	return "", errors.New("no key for " + spec.Name)
}

// decisionsRun checks rules against reviewers that call a Decisions API.
func decisionsRun(t *testing.T, config backend.Config, keys backend.KeySource, project string, rules ...backend.Rule) backend.Run {
	t.Helper()
	store, s := openTest(t)
	s.LoadConfig = func(string) (backend.Config, error) { return config, nil }
	s.Keys = keys
	publish(t, s, backend.Contract{ID: "ci", Name: "CI", Rules: rules})
	run, err := s.StartRun("ci", 1, project)
	if err != nil {
		t.Fatal(err)
	}
	runStores[run.ID] = store
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func decisionsReviewer(name, endpoint string, threshold float64, timeout time.Duration) backend.ReviewerSpec {
	return backend.ReviewerSpec{Name: name, Protocol: backend.DecisionsProtocol, API: backend.APISpec{Endpoint: endpoint, Threshold: threshold, Timeout: timeout}}
}

// fakeDecisions answers each request by the reviewer name in the URL path
// (/<name>/decisions) and records what it received.
func fakeDecisions(t *testing.T) (*httptest.Server, func(name string) ([]byte, string)) {
	bodies, auth := map[string][]byte{}, map[string]string{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[0]
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[name], auth[name] = body, r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/decisions") {
			http.Error(w, "wrong endpoint", http.StatusNotFound)
			return
		}
		answer := func(p string) string {
			return `{"model":"typesafe-ai/jev","answers":[{"type":"predicate","name":"criterion_met","probability":` + p + `}],"usage":{"input_tokens":10}}`
		}
		switch name {
		case "sure", "lenient":
			io.WriteString(w, answer("0.97"))
		case "unsure":
			io.WriteString(w, answer("0.62"))
		case "refuses":
			io.WriteString(w, `{"model":"typesafe-ai/jev","answers":[{"type":"refusal","name":"criterion_met"}]}`)
		case "rejects":
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"Authentication failed","error_type":"authentication_error"}`)
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"provider unavailable","type":"internal_error"}}`)
		case "garbage":
			io.WriteString(w, `<html>gateway error</html>`)
		case "twice":
			io.WriteString(w, `{"answers":[{"type":"predicate","probability":0.9},{"type":"predicate","probability":0.9}]}`)
		case "noprob":
			io.WriteString(w, `{"answers":[{"type":"predicate","name":"criterion_met"}]}`)
		case "badprob":
			io.WriteString(w, answer("1.2"))
		case "slow":
			time.Sleep(2 * time.Second)
			io.WriteString(w, answer("0.99"))
		default:
			http.Error(w, "unknown reviewer", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	received := func(name string) ([]byte, string) {
		mu.Lock()
		defer mu.Unlock()
		return bodies[name], auth[name]
	}
	return server, received
}

func TestDecisionsReviewer(t *testing.T) {
	server, received := fakeDecisions(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello, reader\n"), 0600)
	config := backend.DefaultConfig()
	for _, name := range []string{"sure", "unsure"} {
		config.Reviewers[name] = decisionsReviewer(name, server.URL+"/"+name, 0, 0)
	}
	config.Reviewers["lenient"] = decisionsReviewer("lenient", server.URL+"/lenient", 0.5, 0)
	unsure := config.Reviewers["unsure"]
	unsure.API.Model = "my-decider"
	config.Reviewers["unsure"] = unsure

	run := decisionsRun(t, config, staticKeys{"sure": "sk_test_1"}, project,
		reviewRule("sure", "sure"), reviewRule("unsure", "unsure"), reviewRule("lenient", "lenient"))
	results := run.Attempts[0].Results

	sure := results[0]
	if sure.Status != backend.Passed || sure.Reason != "sure: probability 0.97 that the criterion is met" {
		t.Fatalf("sure: %+v", sure)
	}
	if r := sure.Review; r.Protocol != backend.DecisionsProtocol || r.Verdict != backend.VerdictPass || *r.Probability != 0.97 || r.Model != "typesafe-ai/jev" || r.Sandboxed || r.RequestHash == "" {
		t.Fatalf("sure review: %+v", r)
	}
	sureBody, sureAuth := received("sure")
	unsureBody, unsureAuth := received("unsure")
	if sureAuth != "Bearer sk_test_1" || unsureAuth != "" {
		t.Fatalf("authorization headers: %q %q", sureAuth, unsureAuth)
	}
	// The stored request is exactly what was sent, and carries no key.
	if sent := mustBlob(t, run, sure.Evidence[1]); string(sent) != string(sureBody) || strings.Contains(string(sent), "sk_test_1") {
		t.Fatalf("stored request differs from what was sent:\n%s\n%s", sent, sureBody)
	}
	var request struct {
		Model string `json:"model"`
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Data struct {
					Evidence []backend.ReviewEvidence `json:"evidence"`
				} `json:"data"`
			} `json:"content"`
		} `json:"input"`
		Questions []struct {
			Type, Name, Instructions string
		} `json:"questions"`
	}
	if err := json.Unmarshal(unsureBody, &request); err != nil {
		t.Fatal(err)
	}
	q := request.Questions
	if request.Model != "my-decider" || len(q) != 1 || q[0].Type != "predicate" || q[0].Name != "criterion_met" || !strings.HasSuffix(q[0].Instructions, "Criterion: README greets the reader") {
		t.Fatalf("request: %+v", request)
	}
	if parts := request.Input[0].Content; request.Input[0].Role != "user" || parts[0].Type != "input_text" || parts[1].Type != "input_json" || parts[1].Data.Evidence[0].Content != "Hello, reader\n" {
		t.Fatalf("input: %s", unsureBody)
	}

	if r := results[1]; r.Status != backend.Failed || r.Reason != "unsure: probability 0.62 that the criterion is met; passing needs 0.90" || r.Review.Verdict != backend.VerdictFail {
		t.Fatalf("unsure: %+v", r)
	}
	if r := results[2]; r.Status != backend.Passed {
		t.Fatalf("lenient threshold: %+v", r)
	}
}

func TestDecisionsFailuresAreErrors(t *testing.T) {
	server, received := fakeDecisions(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello\n"), 0600)
	os.WriteFile(filepath.Join(project, "big.txt"), []byte(strings.Repeat("words ", 30000)), 0600)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	config := backend.DefaultConfig()
	names := []string{"refuses", "rejects", "broken", "garbage", "twice", "noprob", "badprob"}
	for _, name := range append(names, "sure") {
		config.Reviewers[name] = decisionsReviewer(name, server.URL+"/"+name, 0, 0)
	}
	config.Reviewers["slow"] = decisionsReviewer("slow", server.URL+"/slow", 0, 300*time.Millisecond)
	config.Reviewers["gone"] = decisionsReviewer("gone", closed.URL, 0, 0)
	config.Reviewers["keyless"] = decisionsReviewer("keyless", "https://decisions.invalid/v1", 0, 0)
	t.Setenv("AI_GATEWAY_API_KEY", "")

	rules := []backend.Rule{}
	for _, name := range append(names, "slow", "gone", "keyless") {
		rules = append(rules, reviewRule(name, name))
	}
	rules = append(rules, reviewRule("too-big", "sure", backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "big.txt"}))
	run := decisionsRun(t, config, backend.EnvKeys{}, project, rules...)

	want := map[string]string{
		"refuses": "refuses's response isn't usable: the model declined to answer",
		"rejects": `reviewer rejects: 127.0.0.1 rejected the API key (HTTP 401: Authentication failed). Check that it's a key for 127.0.0.1, then replace it with "assay reviewer login rejects"`,
		"broken":  "broken returned HTTP 500: provider unavailable",
		"garbage": "expected a Decisions API response",
		"twice":   "expected 1 answer, found 2",
		"noprob":  `the answer has no "probability"`,
		"badprob": `"probability" must be between 0 and 1`,
		"slow":    "reviewer slow timed out after 300ms",
		"gone":    "reviewer gone couldn't be reached at " + closed.URL,
		"keyless": "reviewer keyless has no API key: set AI_GATEWAY_API_KEY",
		"too-big": "the evidence is too large for sure",
	}
	for _, result := range run.Attempts[0].Results {
		if result.Status != backend.OperationalError || !strings.Contains(result.Reason, want[result.RuleID]) {
			t.Errorf("%s: %s %q, want error containing %q", result.RuleID, result.Status, result.Reason, want[result.RuleID])
		}
		sent := false
		for _, item := range result.Evidence {
			sent = sent || item.Kind == backend.ReviewRequestEvidence
		}
		if notSent := result.RuleID == "keyless" || result.RuleID == "too-big"; sent == notSent {
			t.Errorf("%s: request recorded = %v", result.RuleID, sent)
		}
		if result.RuleID == "rejects" && string(mustBlob(t, run, result.Evidence[2])) != `{"message":"Authentication failed","error_type":"authentication_error"}` {
			t.Errorf("rejected response not recorded: %+v", result.Evidence)
		}
	}
	if body, _ := received("sure"); body != nil {
		t.Error("evidence too large to review was sent anyway")
	}
}

// TypeSafe's own API: the evidence is the state, questions are keyed by
// name, and a yes/no answer is "noul".
func TestTypeSafeReviewer(t *testing.T) {
	var mu sync.Mutex
	var got []byte
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, gotAuth, gotPath = body, r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
		switch {
		case strings.Contains(string(body), `"content":"Hello`):
			io.WriteString(w, `{"model":"jev-1.13.0","answers":{"criterion_met":{"type":"noul","noul":0.98}},"usage":{"input_tokens":120,"output_tokens":12}}`)
		case strings.Contains(string(body), `"content":"Garbled`):
			io.WriteString(w, `{"answers":{"other":{"type":"noul","noul":0.98}}}`)
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"detail":[{"loc":["body","state"],"msg":"Field required","type":"missing"}]}`)
		}
	}))
	t.Cleanup(server.Close)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello\n"), 0600)
	os.WriteFile(filepath.Join(project, "garbled.txt"), []byte("Garbled\n"), 0600)
	os.WriteFile(filepath.Join(project, "other.txt"), []byte("Other\n"), 0600)
	config := backend.DefaultConfig()
	config.Reviewers["jev"] = backend.ReviewerSpec{Name: "jev", Protocol: backend.TypeSafeProtocol, API: backend.APISpec{Endpoint: server.URL + "/v1"}}

	run := decisionsRun(t, config, staticKeys{"jev": "apikey_test"}, project,
		reviewRule("hello", "jev"),
		reviewRule("garbled", "jev", backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "garbled.txt"}),
		reviewRule("invalid", "jev", backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "other.txt"}))
	results := run.Attempts[0].Results
	if r := results[0]; r.Status != backend.Passed || r.Reason != "jev: probability 0.98 that the criterion is met" || r.Review.Model != "jev-1.13.0" || r.Review.Protocol != backend.TypeSafeProtocol {
		t.Fatalf("hello: %+v %+v", r, r.Review)
	}
	if r := results[1]; r.Status != backend.OperationalError || !strings.Contains(r.Reason, `expected one answer, to "criterion_met"`) {
		t.Fatalf("garbled: %+v", r)
	}
	if r := results[2]; r.Status != backend.OperationalError || r.Reason != "jev returned HTTP 422: Field required" {
		t.Fatalf("invalid: %+v", r)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/systemone" || gotAuth != "Bearer apikey_test" {
		t.Fatalf("sent to %s with %q", gotPath, gotAuth)
	}
	var request struct {
		Model string `json:"model"`
		State struct {
			Evidence []backend.ReviewEvidence `json:"evidence"`
		} `json:"state"`
		Questions map[string]struct{ Type, Instructions string } `json:"questions"`
	}
	if err := json.Unmarshal(got, &request); err != nil {
		t.Fatal(err)
	}
	q, ok := request.Questions["criterion_met"]
	if request.Model != "jev-latest" || len(request.Questions) != 1 || !ok || q.Type != "noul" || !strings.HasSuffix(q.Instructions, "Criterion: README greets the reader") || request.State.Evidence[0].Content != "Other\n" {
		t.Fatalf("request: %s", got)
	}
}

// A rejected key that came from the environment variable can't be fixed by
// logging in again, since the variable overrides a saved key.
func TestRejectedKeyNamesItsSource(t *testing.T) {
	server, _ := fakeDecisions(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("Hello\n"), 0600)
	config := backend.DefaultConfig()
	config.Reviewers["rejects"] = decisionsReviewer("rejects", server.URL+"/rejects", 0, 0)

	t.Setenv("AI_GATEWAY_API_KEY", "sk_from_env")
	run := decisionsRun(t, config, backend.EnvKeys{}, project, reviewRule("env", "rejects"))
	if reason := run.Attempts[0].Results[0].Reason; !strings.HasSuffix(reason, `fix or unset AI_GATEWAY_API_KEY, which overrides any key saved with "assay reviewer login"`) {
		t.Fatalf("key from the variable: %q", reason)
	}
	t.Setenv("AI_GATEWAY_API_KEY", "")
	run = decisionsRun(t, config, staticKeys{"rejects": "sk_saved"}, project, reviewRule("saved", "rejects"))
	if reason := run.Attempts[0].Results[0].Reason; !strings.HasSuffix(reason, `replace it with "assay reviewer login rejects"`) {
		t.Fatalf("saved key: %q", reason)
	}
}
