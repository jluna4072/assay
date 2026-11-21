package keychain

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// UseTestKeychain points the package at a new, empty keychain file for the
// rest of the test and deletes it afterwards. The user's own keychains
// aren't touched.
func useTestKeychain(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(Tool); err != nil {
		t.Skip("no macOS Keychain on this system")
	}
	path := filepath.Join(t.TempDir(), "test.keychain-db")
	if out, err := exec.Command(Tool, "create-keychain", "-p", "test", path).CombinedOutput(); err != nil {
		t.Fatalf("create keychain: %v\n%s", err, out)
	}
	previous := Path
	Path = path
	t.Cleanup(func() {
		Path = previous
		exec.Command(Tool, "delete-keychain", path).Run()
	})
}

func TestSetGetDelete(t *testing.T) {
	useTestKeychain(t)
	if _, err := Get("harness", "example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get before set: %v", err)
	}
	if err := Set("harness", "example.com", "sk_test_123"); err != nil {
		t.Fatal(err)
	}
	if got, err := Get("harness", "example.com"); err != nil || got != "sk_test_123" {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := Set("harness", "example.com", "sk_test_456"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get("harness", "example.com"); got != "sk_test_456" {
		t.Fatalf("replace: %q", got)
	}
	if _, err := Get("harness", "other.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other account: %v", err)
	}
	if err := Delete("harness", "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("harness", "example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestSetRejectsUnsafeValues(t *testing.T) {
	useTestKeychain(t)
	for _, secret := range []string{"", "two words", "quote\"d", "line\nbreak", `back\slash`} {
		if err := Set("harness", "example.com", secret); err == nil {
			t.Errorf("stored %q", secret)
		}
	}
	if _, err := Get("harness", "example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("something was stored: %v", err)
	}
}
