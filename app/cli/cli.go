// Package cli implements the harness command line. Commands are thin callers
// of the backend; this package owns argument parsing, output, and exit codes.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"golang.org/x/term"
)

// Exit codes. Scripts and agents depend on these; don't renumber them.
const (
	ExitOK          = 0  // success; for checks, every rule passed
	ExitNegative    = 1  // the verdict is negative: invalid contract, failed rule
	ExitOperational = 2  // something couldn't run: unreadable file, broken database
	ExitUsage       = 64 // unknown command, bad flags, missing arguments
)

// JSONSchema versions the --json output shape.
const JSONSchema = 1

// Version is set at release time with -ldflags "-X harness/cli.Version=v0.1.0".
var Version = "dev"

// Env is what a command runs against. DefaultEnv describes the real process;
// tests construct their own.
type Env struct {
	Stdout, Stderr io.Writer
	Dir            string // working directory, where project discovery starts
	Interactive    bool   // a person is at the terminal (stdin is a TTY)
	// ReadSecret reads one line without echoing it when a person is at the
	// terminal, or from stdin otherwise (for piping a key in).
	ReadSecret func(prompt string) (string, error)
}

// DefaultEnv returns the environment of the running process.
func DefaultEnv() Env {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	// A real terminal check: /dev/null is also a character device.
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	return Env{Stdout: os.Stdout, Stderr: os.Stderr, Dir: dir, Interactive: interactive, ReadSecret: func(prompt string) (string, error) {
		if interactive {
			fmt.Fprint(os.Stderr, prompt)
			secret, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(secret), err
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if errors.Is(err, io.EOF) && line != "" {
			err = nil
		}
		return line, err
	}}
}

type command struct {
	name    string // full name, e.g. "contract validate"
	args    string // argument synopsis for help
	summary string
	details string
	run     func(e Env, cmd *command, args []string) int
}

var commands = []command{
	initCommand,
	validateCommand,
	publishCommand,
	versionsCommand,
	showCommand,
	graphCommand,
	checkCommand,
	recheckCommand,
	runsCommand,
	showRunCommand,
	evidenceCommand,
	reviewerListCommand,
	reviewerTestCommand,
	reviewerLoginCommand,
	reviewerLogoutCommand,
}

const overview = `harness checks agent work against contracts you publish.

Usage:
  harness <command> [arguments]

Commands:
%s
Flags:
  -h, --help   Show help for harness or a command
  --version    Print the harness version

Exit codes: 0 success, 1 failed or invalid, 2 operational error, 64 usage error.
Run "harness <command> --help" for details on a command.
`

// Run executes the command line in args (without the program name) and
// returns the process exit code.
func Run(args []string, e Env) int {
	stdout, stderr := e.Stdout, e.Stderr
	if len(args) == 0 {
		printOverview(stderr)
		return ExitUsage
	}
	switch args[0] {
	case "-h", "--help", "-help":
		printOverview(stdout)
		return ExitOK
	case "--version", "-version", "version":
		fmt.Fprintln(stdout, "harness", version())
		return ExitOK
	case "help":
		if len(args) == 1 {
			printOverview(stdout)
			return ExitOK
		}
		if cmd, _ := find(args[1:]); cmd != nil {
			printCommandHelp(stdout, cmd)
			return ExitOK
		}
		return unknown(e, args[1:])
	}
	cmd, rest := find(args)
	if cmd == nil {
		return unknown(e, args)
	}
	return cmd.run(e, cmd, rest)
}

// find matches the longest command name at the start of args.
func find(args []string) (*command, []string) {
	var best *command
	bestWords := 0
	for i := range commands {
		words := strings.Fields(commands[i].name)
		if len(words) <= len(args) && len(words) > bestWords && equalWords(words, args[:len(words)]) {
			best, bestWords = &commands[i], len(words)
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, args[bestWords:]
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unknown reports an unknown command, listing subcommands when args name a group.
func unknown(e Env, args []string) int {
	var group []command
	for _, cmd := range commands {
		if strings.HasPrefix(cmd.name+" ", args[0]+" ") {
			group = append(group, cmd)
		}
	}
	if len(group) > 0 && (len(args) == 1 || strings.HasPrefix(args[1], "-")) {
		w := e.Stderr
		code := ExitUsage
		if len(args) > 1 && (args[1] == "-h" || args[1] == "--help" || args[1] == "-help") {
			w, code = e.Stdout, ExitOK
		}
		fmt.Fprintf(w, "Usage:\n  harness %s <command> [arguments]\n\nCommands:\n%s", args[0], commandList(group))
		return code
	}
	fmt.Fprintf(e.Stderr, "harness: unknown command %q\nRun \"harness --help\" for usage.\n", strings.Join(args, " "))
	return ExitUsage
}

func printOverview(w io.Writer) { fmt.Fprintf(w, overview, commandList(commands)) }

func commandList(list []command) string {
	width := 0
	for _, cmd := range list {
		width = max(width, len(cmd.name)+1+len(cmd.args))
	}
	var b strings.Builder
	for _, cmd := range list {
		fmt.Fprintf(&b, "  %-*s   %s\n", width, cmd.name+" "+cmd.args, cmd.summary)
	}
	return b.String()
}

func printCommandHelp(w io.Writer, cmd *command) {
	fmt.Fprintf(w, "Usage:\n  harness %s %s\n\n%s\n", cmd.name, cmd.args, strings.TrimSpace(cmd.details))
}

// parseFlags parses flags that may appear before, between, or after
// positional arguments, and returns the positional ones. "--" ends flags.
func parseFlags(e Env, cmd *command, fs *flag.FlagSet, args []string) ([]string, int, bool) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		err := fs.Parse(args)
		if errors.Is(err, flag.ErrHelp) {
			printCommandHelp(e.Stdout, cmd)
			return nil, ExitOK, false
		}
		if err != nil {
			fmt.Fprintf(e.Stderr, "harness %s: %v\nRun \"harness %s --help\" for usage.\n", cmd.name, err, cmd.name)
			return nil, ExitUsage, false
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, ExitOK, true
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), ExitOK, true
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	encoder.Encode(value)
}

func version() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	revision, modified := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return Version
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified {
		revision += "-dirty"
	}
	return Version + " (" + revision + ")"
}
