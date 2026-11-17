// Package runner executes tasks.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/moznion/uki/internal/config"
)

type Result struct {
	Name      string
	Command   string
	Success   bool
	ExitCode  int
	StartedAt time.Time
	Duration  time.Duration
	LogSHA256 string
}

// Options tune how a task is run.
type Options struct {
	// Detach runs the task in its own process group. Cancelling ctx then
	// signals the whole group (the shell and everything it started), so a
	// task stopped while others keep running leaves nothing behind. It is
	// meant for tasks run in parallel, whose output is not a terminal; a
	// detached task cannot read from or prompt on the terminal.
	Detach bool
}

// Run executes the task via `sh -c` in dir, streaming its output to stdout/stderr.
func Run(ctx context.Context, dir, name string, t config.Task, stdout, stderr io.Writer) Result {
	return RunWith(ctx, dir, name, t, stdout, stderr, Options{})
}

// RunWith is Run with options.
func RunWith(ctx context.Context, dir, name string, t config.Task, stdout, stderr io.Writer, opts Options) Result {
	fmt.Fprintf(stderr, "==> [%s] %s\n", name, t.Run)

	// exec copies stdout and stderr on separate goroutines, so the shared
	// hash must be guarded. The interleaving order is not deterministic.
	h := &lockedWriter{w: sha256.New()}
	// Running the commands from uki.yaml is the purpose of a task runner; the
	// config comes from the repository the user chose to run uki in.
	cmd := exec.CommandContext(ctx, "sh", "-c", t.Run) //nolint:gosec // see above
	cmd.Dir = dir
	cmd.Stdout = io.MultiWriter(stdout, h)
	cmd.Stderr = io.MultiWriter(stderr, h)
	cmd.Env = os.Environ()
	if opts.Detach {
		detach(cmd)
	}
	keys := make([]string, 0, len(t.Env))
	for k := range t.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+t.Env[k])
	}

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Name:      name,
		Command:   t.Run,
		StartedAt: start.UTC(),
		Duration:  time.Since(start),
		LogSHA256: hex.EncodeToString(h.w.Sum(nil)),
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Success = true
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		fmt.Fprintf(stderr, "uki: [%s] failed to start: %v\n", name, err)
		res.ExitCode = -1
	}

	status := "ok"
	if !res.Success {
		status = fmt.Sprintf("FAILED (exit %d)", res.ExitCode)
	}
	fmt.Fprintf(stderr, "==> [%s] %s in %s\n", name, status, res.Duration.Round(time.Millisecond))
	return res
}

type lockedWriter struct {
	mu sync.Mutex
	w  hash.Hash
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
