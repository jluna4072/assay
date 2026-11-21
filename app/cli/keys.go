package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"assay/backend"
	"assay/keychain"
	"assay/project"
)

// keychainService names assay's items in the macOS Keychain. Each item's
// account is the API host, so reviewers on one provider share a key.
const keychainService = "assay"

// cliKeys finds reviewer API keys: the reviewer's environment variable
// first, then the Keychain. When a person is at the terminal, a missing key
// is asked for once and saved, so it never has to be set up again.
type cliKeys struct {
	e      Env
	prompt bool              // ask for a missing key
	asked  map[string]string // keys entered this run, by host
}

func newKeys(e Env, prompt bool) *cliKeys {
	return &cliKeys{e: e, prompt: prompt && e.Interactive && e.ReadSecret != nil, asked: map[string]string{}}
}

// keySource says where a reviewer's key would come from, without asking
// for one.
func keySource(spec backend.ReviewerSpec) string {
	if !backend.UsesAPIKey(spec.Protocol) {
		return ""
	}
	d := spec.Settings()
	switch _, err := keychain.Get(keychainService, d.Host()); {
	case strings.TrimSpace(os.Getenv(d.KeyEnv)) != "":
		return "$" + d.KeyEnv
	case err == nil:
		return "keychain"
	case d.Local():
		return "not needed"
	}
	return "missing"
}

func (k *cliKeys) Key(spec backend.ReviewerSpec) (string, error) {
	d := spec.Settings()
	if key := strings.TrimSpace(os.Getenv(d.KeyEnv)); key != "" {
		return key, nil
	}
	host := d.Host()
	if key, ok := k.asked[host]; ok {
		return key, nil
	}
	key, err := keychain.Get(keychainService, host)
	switch {
	case err == nil && key != "":
		return key, nil
	case err != nil && !errors.Is(err, keychain.ErrNotFound) && !errors.Is(err, keychain.ErrUnavailable):
		return "", fmt.Errorf("reviewer %s: %v", spec.Name, err)
	case d.Local():
		return "", nil
	case !k.prompt:
		return "", fmt.Errorf("reviewer %s has no API key for %s. Run \"assay reviewer login %s\" or set %s", spec.Name, host, spec.Name, d.KeyEnv)
	}

	fmt.Fprintf(k.e.Stderr, "Reviewer %s needs an API key for %s, and none is set up yet.\n", spec.Name, host)
	key, err = k.readKey()
	if err != nil {
		return "", fmt.Errorf("reviewer %s: %v", spec.Name, err)
	}
	k.asked[host] = key
	if err := keychain.Set(keychainService, host, key); err != nil {
		fmt.Fprintf(k.e.Stderr, "Couldn't save it to your Keychain (%v); using it for this run only.\n", err)
	} else {
		fmt.Fprintln(k.e.Stderr, "Saved to your Keychain; assay won't ask again on this Mac.")
	}
	return key, nil
}

// readKey asks for a key with hidden input.
func (k *cliKeys) readKey() (string, error) {
	key, err := k.e.ReadSecret("Paste the key (input is hidden): ")
	if err != nil {
		return "", fmt.Errorf("read the key: %v", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("no key was entered")
	}
	return key, nil
}

var reviewerLoginCommand = command{
	name:    "reviewer login",
	args:    "[name]",
	summary: "Save a reviewer's API key in the macOS Keychain",
	details: `
Asks for the API key of a reviewer (the default reviewer without a name)
and saves it in your macOS Keychain, replacing any saved key. assay also
asks for a missing key the first time a review needs one, so this is only
needed to set a key up ahead of time or replace it.

Keys are saved per API host, so reviewers that use the same endpoint share
one. The reviewer's environment variable (TYPESAFE_API_KEY for a typesafe
reviewer), when set, takes precedence over a saved key. With stdin not a
terminal, the key is read from stdin:
  printf %s "$KEY" | assay reviewer login

Flags:
` + projectFlagHelp + `

Exit codes: 0 saved, 2 the key couldn't be read or saved, 64 usage error
or a reviewer that doesn't use an API key.
`,
	run: runReviewerLogin,
}

var reviewerLogoutCommand = command{
	name:    "reviewer logout",
	args:    "[name]",
	summary: "Remove a reviewer's saved API key from the macOS Keychain",
	details: `
Removes the API key saved for a reviewer's API host (the default reviewer
without a name). Other reviewers on the same host lose it too. Keys in
environment variables aren't affected.

Flags:
` + projectFlagHelp + `

Exit codes: 0 removed or there was none, 2 the Keychain couldn't be
changed, 64 usage error or a reviewer that doesn't use an API key.
`,
	run: runReviewerLogout,
}

// keyedReviewer finds the named (or default) reviewer and checks that it
// uses an API key.
func keyedReviewer(e Env, cmd *command, args []string) (backend.ReviewerSpec, int) {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	projectDir := fs.String("project", "", "")
	names, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return backend.ReviewerSpec{}, code
	}
	if len(names) > 1 {
		fmt.Fprintf(e.Stderr, "assay %s: name at most one reviewer\n", cmd.name)
		return backend.ReviewerSpec{}, ExitUsage
	}
	name := ""
	if len(names) == 1 {
		name = names[0]
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return backend.ReviewerSpec{}, code
	}
	defer s.Close()
	config, err := project.LoadConfig(s.project.Root)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return backend.ReviewerSpec{}, ExitOperational
	}
	spec, err := config.Reviewer(name)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return backend.ReviewerSpec{}, ExitUsage
	}
	if !backend.UsesAPIKey(spec.Protocol) {
		fmt.Fprintf(e.Stderr, "assay %s: reviewer %s runs a program, which gets any keys it needs from the variables listed under env: in its config\n", cmd.name, spec.Name)
		return backend.ReviewerSpec{}, ExitUsage
	}
	return spec, ExitOK
}

func runReviewerLogin(e Env, cmd *command, args []string) int {
	spec, code := keyedReviewer(e, cmd, args)
	if code != ExitOK {
		return code
	}
	if e.ReadSecret == nil {
		fmt.Fprintf(e.Stderr, "assay %s: can't read a key here\n", cmd.name)
		return ExitOperational
	}
	d := spec.Settings()
	if e.Interactive {
		fmt.Fprintf(e.Stderr, "API key for %s (used by reviewer %s).\n", d.Host(), spec.Name)
	}
	key, err := (&cliKeys{e: e}).readKey()
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return ExitOperational
	}
	if err := keychain.Set(keychainService, d.Host(), key); err != nil {
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return ExitOperational
	}
	fmt.Fprintf(e.Stdout, "Saved the API key for %s to your Keychain.\n", d.Host())
	if strings.TrimSpace(os.Getenv(d.KeyEnv)) != "" {
		fmt.Fprintf(e.Stdout, "%s is set in this shell and takes precedence while it is.\n", d.KeyEnv)
	}
	return ExitOK
}

func runReviewerLogout(e Env, cmd *command, args []string) int {
	spec, code := keyedReviewer(e, cmd, args)
	if code != ExitOK {
		return code
	}
	host := spec.Settings().Host()
	switch err := keychain.Delete(keychainService, host); {
	case errors.Is(err, keychain.ErrNotFound):
		fmt.Fprintf(e.Stdout, "No API key was saved for %s.\n", host)
	case err != nil:
		fmt.Fprintf(e.Stderr, "assay %s: %v\n", cmd.name, err)
		return ExitOperational
	default:
		fmt.Fprintf(e.Stdout, "Removed the API key for %s from your Keychain.\n", host)
	}
	return ExitOK
}
