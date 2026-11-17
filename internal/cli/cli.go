// Package cli implements the uki subcommands.
package cli

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
)

//go:embed templates/*
var templates embed.FS

// WorkflowPath is where `uki init` writes the GitHub workflow.
const WorkflowPath = ".github/workflows/uki.yml"

// ActionsLockPath is the GitHub Actions workflow lockfile. Pushes to the
// records branch run the workflow from that branch, so when the repository
// uses a lockfile it must be seeded there too.
const ActionsLockPath = ".github/workflows/actions.lock"

// Version is overridable via -ldflags "-X github.com/moznion/uki/internal/cli.Version=...".
var Version = ""

func version() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// app carries the output streams so commands can be exercised in tests.
type app struct {
	stdout io.Writer
	stderr io.Writer
}

// errUsage marks command-line errors; the flag package has already printed
// the details and usage, so Main only maps it to exit code 2.
var errUsage = errors.New("usage error")

func (a *app) flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("uki "+name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	return fs
}

// parse parses args, returning flag.ErrHelp for -h or errUsage otherwise.
func (a *app) parse(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, flag.ErrHelp):
		return flag.ErrHelp
	default:
		return errUsage
	}
}

func (a *app) warn(msg string) {
	fmt.Fprintf(a.stderr, "uki: WARNING: %s\n", msg)
}

// Main dispatches subcommands and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	a := &app{stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "run":
		code, err = a.runCmd(args[1:])
	case "status":
		code, err = a.statusCmd(args[1:])
	case "mark":
		err = a.markCmd(args[1:])
	case "init":
		err = a.initCmd(args[1:])
	case "config":
		err = a.configCmd(args[1:])
	case "version", "--version":
		fmt.Fprintln(stdout, version())
	case "-h", "--help", "help":
		usage(stderr)
	default:
		fmt.Fprintf(stderr, "uki: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "uki: %v\n", err)
		return 1
	}
	return code
}

func usage(w io.Writer) {
	fmt.Fprint(w, `uki - run CI locally, publish the results to GitHub

Usage:
  uki run [flags] [task...]   run tasks (default: tasks for the current branch) and record the result
  uki status [flags]          show the record and verdict for HEAD (exit 0=success, 1=failure/error, 3=pending)
  uki mark [flags]            (GitHub Actions) set commit statuses from records
  uki init                    write uki.yaml and the GitHub workflow
  uki config get <key>        print a setting from uki.yaml (e.g. record.branch)
  uki version

Run "uki <command> -h" for flags.
`)
}
