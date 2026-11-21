package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"harness/keychain"
)

// TestMain points the Keychain at a throwaway keychain file, so no test
// reads or writes the user's own keys.
func TestMain(m *testing.M) {
	os.Unsetenv("AI_GATEWAY_API_KEY")
	dir, err := os.MkdirTemp("", "harness-cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path := filepath.Join(dir, "test.keychain-db")
	if _, err := os.Stat(keychain.Tool); err == nil {
		if out, err := exec.Command(keychain.Tool, "create-keychain", "-p", "test", path).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "create test keychain: %v\n%s", err, out)
			os.Exit(1)
		}
		keychain.Path = path
	}
	code := m.Run()
	if keychain.Path != "" {
		exec.Command(keychain.Tool, "delete-keychain", path).Run()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}
