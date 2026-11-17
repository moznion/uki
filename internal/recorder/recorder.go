// Package recorder commits task results to the records branch and pushes them.
package recorder

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/repofile"
)

type Recorder struct {
	Git    gitx.Git // the user's repository
	Remote string
	Branch string
	Sign   bool
	// SeedFiles are repo-relative paths copied from the user's worktree when the
	// records branch is first created (e.g. the workflow file, so that pushes to
	// the records branch trigger it).
	SeedFiles []string
	MaxTries  int
	// Log receives progress messages; nil discards them.
	Log io.Writer
}

// Artifacts maps a task name to its artifacts: each repository-relative path
// (as listed in the record) to a local file holding the content to store.
type Artifacts map[string]map[string]string

// Save merges results into the record for sha and pushes it, retrying on
// non-fast-forward by re-fetching and re-applying the merge.
//
// Every task in results replaces its stored artifacts with the ones in
// artifacts (none if it has no entry), so a re-run never leaves stale files.
func (r Recorder) Save(sha, tree string, results map[string]record.TaskResult, artifacts Artifacts) error {
	if !record.ValidSHA(sha) {
		return fmt.Errorf("invalid commit SHA %q", sha)
	}
	log := r.Log
	if log == nil {
		log = io.Discard
	}
	tries := r.MaxTries
	if tries <= 0 {
		tries = 10
	}

	dir, err := os.MkdirTemp("", "uki-records-*")
	if err != nil {
		return err
	}
	_ = os.Remove(dir) // git worktree add wants to create it
	tmpBranch := ""
	defer func() {
		_, _ = r.Git.Run("worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
		if tmpBranch != "" {
			_, _ = r.Git.Run("branch", "-D", tmpBranch)
		}
	}()

	exists, err := r.Git.RemoteBranchExists(r.Remote, r.Branch)
	if err != nil {
		return err
	}
	var remoteRef string
	if exists {
		if remoteRef, err = r.Git.FetchBranch(r.Remote, r.Branch); err != nil {
			return err
		}
		if _, err := r.Git.Run("worktree", "add", "--quiet", "--detach", dir, remoteRef); err != nil {
			return err
		}
	} else {
		tmpBranch = fmt.Sprintf("uki-records-tmp-%d", os.Getpid())
		if _, err := r.Git.Run("worktree", "add", "--quiet", "--orphan", "-b", tmpBranch, dir); err != nil {
			return err
		}
	}
	wt := gitx.Git{Dir: dir}

	var lastErr error
	for attempt := 1; attempt <= tries; attempt++ {
		if attempt > 1 {
			fmt.Fprintf(log, "uki: push rejected, retrying (%d/%d)\n", attempt, tries)
			// Jittered backoff so concurrent recorders don't keep colliding.
			time.Sleep(time.Duration(attempt*100+rand.IntN(500)) * time.Millisecond) //nolint:gosec // jitter, not security-sensitive
			exists, err = r.Git.RemoteBranchExists(r.Remote, r.Branch)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("push records: %w", lastErr)
			}
			if remoteRef, err = r.Git.FetchBranch(r.Remote, r.Branch); err != nil {
				return err
			}
			if _, err := wt.Run("reset", "--quiet", "--hard", remoteRef); err != nil {
				return err
			}
			// Leaving orphan mode: the seed files now come from the remote.
			exists = true
		}

		if err := r.writeRecord(dir, sha, tree, results, !exists); err != nil {
			return err
		}
		if err := writeArtifacts(dir, sha, results, artifacts); err != nil {
			return err
		}
		if _, err := wt.Run("add", "-A"); err != nil {
			return err
		}
		args := []string{"commit", "--quiet", "-m", commitMessage(sha, tree, results)}
		if r.Sign {
			args = append(args, "-S")
		}
		if _, err := wt.Run(args...); err != nil {
			return err
		}
		// --no-verify: the records branch holds generated JSON only. Running
		// the user's pre-push hook here would re-enter hooks that call uki
		// (from a worktree without uki.yaml) and could block recording.
		if _, lastErr = wt.Run("push", "--quiet", "--no-verify", r.Remote, "HEAD:refs/heads/"+r.Branch); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("push records after %d tries: %w", tries, lastErr)
}

func (r Recorder) writeRecord(dir, sha, tree string, results map[string]record.TaskResult, seed bool) error {
	path := filepath.Join(dir, record.Path(sha))
	var existing *record.Record
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // path is built from a validated SHA inside our temp worktree
		if existing, err = record.Parse(b); err != nil {
			return fmt.Errorf("parse existing record %s: %w", record.Path(sha), err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	b, err := record.Merge(existing, sha, tree, results).Marshal()
	if err != nil {
		return err
	}
	if err := repofile.Write(path, b); err != nil {
		return err
	}

	if seed {
		top, err := r.Git.TopLevel()
		if err != nil {
			return err
		}
		for _, f := range r.SeedFiles {
			src, err := os.ReadFile(filepath.Join(top, f)) //nolint:gosec // SeedFiles are fixed repo-relative paths set by uki
			if errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if err := repofile.Write(filepath.Join(dir, f), src); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeArtifacts(dir, sha string, results map[string]record.TaskResult, artifacts Artifacts) error {
	for task := range results {
		// Drop what an earlier run of the task stored for this commit.
		if config.ValidArtifactTaskName(task) {
			if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(record.ArtifactDir(sha, task)))); err != nil {
				return err
			}
		}
		files := artifacts[task]
		if len(files) == 0 {
			continue
		}
		if !config.ValidArtifactTaskName(task) {
			return fmt.Errorf("task %q cannot store artifacts", task)
		}
		for p, src := range files {
			if !config.ValidArtifactPath(p) {
				return fmt.Errorf("task %q: invalid artifact path %q", task, p)
			}
			if _, ok := results[task].Artifacts[p]; !ok {
				return fmt.Errorf("task %q: artifact %q is not listed in the result", task, p)
			}
			dst := filepath.Join(dir, filepath.FromSlash(record.ArtifactPath(sha, task, p)))
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("store artifact %s of %s: %w", p, task, err)
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is a file uki staged itself
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // tracked files on the records branch; see repofile
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // see above
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func commitMessage(sha, tree string, results map[string]record.TaskResult) string {
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)
	summary := make([]string, 0, len(names))
	for _, n := range names {
		s := "ok"
		if !results[n].Success {
			s = "failed"
		}
		summary = append(summary, n+":"+s)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "uki: record %s (%s)\n\n", sha[:12], strings.Join(summary, ", "))
	fmt.Fprintf(&b, "Uki-Target: %s\n", sha)
	fmt.Fprintf(&b, "Uki-Tree: %s\n", tree)
	for _, n := range names {
		res := results[n]
		fmt.Fprintf(&b, "Uki-Task: %s success=%t exit=%d config=%s log=%s\n",
			n, res.Success, res.ExitCode, res.ConfigSHA256, res.LogSHA256)
	}
	return b.String()
}
