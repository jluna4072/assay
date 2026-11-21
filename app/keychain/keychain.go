// Package keychain stores secrets in the macOS Keychain through Apple's
// /usr/bin/security tool. Using the tool rather than the Security framework
// means the Keychain item is owned by security, so macOS doesn't ask again
// whenever the assay binary changes. A secret is written to the tool's
// stdin (in its interactive mode), never passed as an argument, so it can't
// be seen in the process list.
package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNotFound means there's no item for the service and account.
var ErrNotFound = errors.New("not in the keychain")

// ErrUnavailable means the Keychain can't be used on this system.
var ErrUnavailable = errors.New("the macOS Keychain isn't available on this system")

// Tool is the security program. Path, when set, is a keychain file to use
// instead of the user's default keychain (tests use a throwaway one).
var (
	Tool = "/usr/bin/security"
	Path = ""
)

func available() error {
	if _, err := os.Stat(Tool); err != nil {
		return ErrUnavailable
	}
	return nil
}

// plain reports whether s can be written as one token of a security
// command: the interactive mode splits on whitespace and treats quotes and
// backslashes specially.
func plain(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t\r\n\"'\\")
}

func withPath(args ...string) []string {
	if Path != "" {
		args = append(args, Path)
	}
	return args
}

// Get returns the secret stored for service and account.
func Get(service, account string) (string, error) {
	if err := available(); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(Tool, withPath("find-generic-password", "-s", service, "-a", account, "-w")...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read the keychain: %s", message(stderr, err))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// Set stores secret for service and account, replacing any existing one.
func Set(service, account, secret string) error {
	if err := available(); err != nil {
		return err
	}
	if !plain(service) || !plain(account) || (Path != "" && !plain(Path)) {
		return fmt.Errorf("can't store a keychain item named %q for %q", service, account)
	}
	if !plain(secret) {
		return errors.New("the key contains spaces, quotes, or backslashes, which API keys don't; check that it was pasted correctly")
	}
	line := strings.Join(withPath("add-generic-password", "-U", "-s", service, "-a", account, "-w", secret), " ") + "\n"
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(Tool, "-i")
	cmd.Stdin = strings.NewReader(line)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stderr.Len() > 0 {
		return fmt.Errorf("write the keychain: %s", message(stderr, err))
	}
	// Interactive mode reports some failures without a non-zero exit, so
	// confirm the item is there.
	stored, err := Get(service, account)
	if err != nil {
		return err
	}
	if stored != secret {
		return errors.New("write the keychain: the stored key doesn't match")
	}
	return nil
}

// Delete removes the item for service and account. Deleting an item that
// doesn't exist returns ErrNotFound.
func Delete(service, account string) error {
	if err := available(); err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.Command(Tool, withPath("delete-generic-password", "-s", service, "-a", account)...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete from the keychain: %s", message(stderr, err))
	}
	return nil
}

func message(stderr bytes.Buffer, err error) string {
	if text := strings.TrimSpace(stderr.String()); text != "" {
		return text
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
