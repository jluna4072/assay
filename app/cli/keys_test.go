package cli

import (
	"bytes"
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

	"harness/keychain"
)

func requireKeychain(t *testing.T) {
	t.Helper()
	if keychain.Path == "" {
		t.Skip("no macOS Keychain on this system")
	}
}

// fakeJev answers like a Decisions API: likely met when the evidence says
// hello. It records the Authorization header of the last request.
func fakeJev(t *testing.T) (*httptest.Server, func() string) {
	var mu sync.Mutex
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		p := "0.12"
		if bytes.Contains(body, []byte(`"content":"Hello`)) {
			p = "0.96"
		}
		io.WriteString(w, `{"model":"typesafe-ai/jev","answers":[{"type":"predicate","name":"criterion_met","probability":`+p+`}]}`)
	}))
	t.Cleanup(server.Close)
	return server, func() string { mu.Lock(); defer mu.Unlock(); return auth }
}

// runWithSecret runs harness as if a person typed answers at the terminal
// (or piped them, when interactive is false), and counts how often harness
// asked.
func runWithSecret(dir string, interactive bool, answer string, args ...string) (stdout, stderr string, code, asked int) {
	var out, errOut bytes.Buffer
	env := Env{Stdout: &out, Stderr: &errOut, Dir: dir, Interactive: interactive, ReadSecret: func(prompt string) (string, error) {
		asked++
		errOut.WriteString(prompt)
		if answer == "" {
			return "", errors.New("no input")
		}
		return answer, nil
	}}
	code = Run(args, env)
	return out.String(), errOut.String(), code, asked
}

func jevProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "init")
	config := "version: 1\nreviewers:\n  jev:\n    protocol: decisions\n    endpoint: " + endpoint + "\n"
	os.WriteFile(filepath.Join(dir, ".harness", "config.yaml"), []byte(config), 0600)
	os.WriteFile(filepath.Join(dir, "harness", "greet.yaml"), []byte(reviewContract), 0644)
	mustRun(t, dir, "contract", "publish", "harness/greet.yaml", "--yes")
	return dir
}

func TestJevReviewer(t *testing.T) {
	server, _ := fakeJev(t)
	dir := jevProject(t, server.URL+"/v1")

	list := mustRun(t, dir, "reviewer", "list")
	if !strings.Contains(list, "jev (default)  decisions  typesafe-ai/jev ≥ 0.90  not needed  2m0s") {
		t.Fatalf("list:\n%s", list)
	}
	if out := mustRun(t, dir, "reviewer", "test"); !strings.Contains(out, "jev works") || !strings.Contains(out, "pass · p=0.96 · model typesafe-ai/jev") {
		t.Fatalf("test:\n%s", out)
	}

	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Goodbye\n"), 0644)
	out, _, code := runIn(dir, false, "check", "greet")
	if code != ExitNegative || !strings.Contains(out, "✗ says-hello   jev: probability 0.12 that the criterion is met; passing needs 0.90") {
		t.Fatalf("check: exit %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Hello\n"), 0644)
	if out := mustRun(t, dir, "recheck"); !strings.Contains(out, "✓ says-hello   jev: probability 0.96 that the criterion is met") {
		t.Fatalf("recheck:\n%s", out)
	}
	var shown struct {
		Run runJSON `json:"run"`
	}
	json.Unmarshal([]byte(mustRun(t, dir, "show", "--json")), &shown)
	if r := shown.Run.Attempts[1].Results[0].Review; r == nil || r.Protocol != "decisions" || *r.Probability != 0.96 {
		t.Fatalf("json review: %+v", r)
	}
}

func TestReviewerLoginAndLogout(t *testing.T) {
	requireKeychain(t)
	server, auth := fakeJev(t)
	dir := jevProject(t, server.URL+"/v1")
	host := "127.0.0.1"

	out, stderr, code, _ := runWithSecret(dir, true, "sk_test_login", "reviewer", "login")
	if code != ExitOK || !strings.Contains(out, "Saved the API key for "+host) || !strings.Contains(stderr, "(input is hidden)") {
		t.Fatalf("login: exit %d\n%s%s", code, out, stderr)
	}
	if key, err := keychain.Get(keychainService, host); err != nil || key != "sk_test_login" {
		t.Fatalf("stored: %q %v", key, err)
	}
	if list := mustRun(t, dir, "reviewer", "list"); !strings.Contains(list, "keychain") {
		t.Fatalf("list:\n%s", list)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Hello\n"), 0644)
	mustRun(t, dir, "check", "greet")
	if got := auth(); got != "Bearer sk_test_login" {
		t.Fatalf("authorization: %q", got)
	}
	// The variable takes precedence.
	t.Setenv("AI_GATEWAY_API_KEY", "sk_test_env")
	mustRun(t, dir, "recheck")
	if got := auth(); got != "Bearer sk_test_env" {
		t.Fatalf("authorization with variable: %q", got)
	}
	os.Unsetenv("AI_GATEWAY_API_KEY")

	// Piped input, as in: printf %s "$KEY" | harness reviewer login
	if _, _, code, _ := runWithSecret(dir, false, "sk_test_piped\n", "reviewer", "login", "jev"); code != ExitOK {
		t.Fatalf("piped login: exit %d", code)
	}
	if key, _ := keychain.Get(keychainService, host); key != "sk_test_piped" {
		t.Fatalf("piped key: %q", key)
	}
	if _, stderr, code, _ := runWithSecret(dir, false, "two words", "reviewer", "login"); code != ExitOperational || !strings.Contains(stderr, "pasted correctly") {
		t.Fatalf("bad key: exit %d\n%s", code, stderr)
	}

	if out := mustRun(t, dir, "reviewer", "logout"); !strings.Contains(out, "Removed the API key for "+host) {
		t.Fatalf("logout: %s", out)
	}
	if out := mustRun(t, dir, "reviewer", "logout"); !strings.Contains(out, "No API key was saved for "+host) {
		t.Fatalf("logout twice: %s", out)
	}
	// Command reviewers get keys through env:, not login.
	os.WriteFile(filepath.Join(dir, ".harness", "config.yaml"), []byte(reviewerConfig), 0600)
	if _, stderr, code, _ := runWithSecret(dir, true, "k", "reviewer", "login", "fake"); code != ExitUsage || !strings.Contains(stderr, "runs a program") {
		t.Fatalf("command reviewer login: exit %d\n%s", code, stderr)
	}
}

// A remote reviewer without a key asks for one once, at the terminal only.
func TestMissingKeyIsAskedForOnce(t *testing.T) {
	requireKeychain(t)
	dir := jevProject(t, "https://decisions.invalid/v1")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Hello\n"), 0644)
	t.Cleanup(func() { keychain.Delete(keychainService, "decisions.invalid") })

	// Not at a terminal, or with --json: never ask.
	out, _, code, asked := runWithSecret(dir, false, "sk_test_x", "check", "greet")
	if asked != 0 || code != ExitOperational || !strings.Contains(out, `reviewer jev has no API key for decisions.invalid. Run "harness reviewer login jev" or set AI_GATEWAY_API_KEY`) {
		t.Fatalf("non-interactive: exit %d, asked %d\n%s", code, asked, out)
	}
	if _, _, _, asked := runWithSecret(dir, true, "sk_test_x", "recheck", "--json"); asked != 0 {
		t.Fatal("asked for a key with --json")
	}
	if list := mustRun(t, dir, "reviewer", "list"); !strings.Contains(list, "missing") {
		t.Fatalf("list:\n%s", list)
	}

	// At a terminal: ask, save, and carry on (the endpoint itself doesn't exist).
	out, stderr, code, asked := runWithSecret(dir, true, "sk_test_first", "recheck")
	if asked != 1 || !strings.Contains(stderr, "Reviewer jev needs an API key for decisions.invalid") || !strings.Contains(stderr, "Saved to your Keychain") {
		t.Fatalf("first use: exit %d, asked %d\n%s%s", code, asked, out, stderr)
	}
	if code != ExitOperational || !strings.Contains(out, "couldn't be reached") {
		t.Fatalf("first use result: exit %d\n%s", code, out)
	}
	if key, _ := keychain.Get(keychainService, "decisions.invalid"); key != "sk_test_first" {
		t.Fatalf("saved key: %q", key)
	}
	// Never asked again.
	if _, _, _, asked := runWithSecret(dir, true, "sk_test_second", "recheck"); asked != 0 {
		t.Fatalf("asked again")
	}
	// An empty answer is an error, not a key.
	keychain.Delete(keychainService, "decisions.invalid")
	if out, _, _, _ := runWithSecret(dir, true, "", "recheck"); !strings.Contains(out, "read the key") {
		t.Fatalf("no answer:\n%s", out)
	}
}

func TestTypeSafeReviewerListing(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init")
	os.WriteFile(filepath.Join(dir, ".harness", "config.yaml"), []byte("version: 1\nreviewers:\n  jev:\n    protocol: typesafe\n"), 0600)
	t.Setenv("TYPESAFE_API_KEY", "apikey_test")
	if list := mustRun(t, dir, "reviewer", "list"); !strings.Contains(list, "jev (default)  typesafe  jev-latest ≥ 0.90  $TYPESAFE_API_KEY") {
		t.Fatalf("list:\n%s", list)
	}
}
