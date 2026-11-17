package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/recorder"
)

// checkArtifactsUntracked refuses artifacts that git tracks: their content is
// already in the commit, and a task rewriting them would dirty the tree.
func checkArtifactsUntracked(g gitx.Git, cfg *config.Config, tasks []string) error {
	for _, name := range tasks {
		for _, p := range cfg.Tasks[name].Artifacts {
			out, err := g.Run("ls-files", "--cached", "--", ":(literal)"+p)
			if err != nil {
				return err
			}
			if out != "" {
				return fmt.Errorf("task %q: artifact %q is tracked by git; artifacts must be ignored files (add it to .gitignore and git rm --cached it)", name, p)
			}
		}
	}
	return nil
}

// artifactStage copies artifacts out of the working tree as each task ends,
// so they survive later tasks and can be written to the records branch.
type artifactStage struct {
	top      string // resolved repository root
	dir      string // staging directory
	maxBytes int64
	files    recorder.Artifacts
	meta     map[string]map[string]record.Artifact
}

func newArtifactStage(top string, maxBytes int64) (*artifactStage, error) {
	resolved, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "uki-artifacts-*")
	if err != nil {
		return nil, err
	}
	return &artifactStage{
		top:      resolved,
		dir:      dir,
		maxBytes: maxBytes,
		files:    recorder.Artifacts{},
		meta:     map[string]map[string]record.Artifact{},
	}, nil
}

func (s *artifactStage) cleanup() { _ = os.RemoveAll(s.dir) }

// collect stages the artifacts of task. Files that cannot be stored are
// reported through warn and left out; the task result itself is unaffected.
func (s *artifactStage) collect(task string, paths []string, warn func(string)) error {
	// A re-listed task (uki run lint lint) keeps only its last run's files.
	delete(s.files, task)
	delete(s.meta, task)
	for i, p := range paths {
		src, err := s.locate(p)
		if err != nil {
			warn(fmt.Sprintf("[%s] artifact %s not stored: %v", task, p, err))
			continue
		}
		dst := filepath.Join(s.dir, fmt.Sprintf("%s-%d", hex.EncodeToString([]byte(task)), i))
		a, err := s.copy(src, dst)
		if errors.Is(err, errTooLarge) {
			warn(fmt.Sprintf("[%s] artifact %s not stored: larger than record.max_artifact_bytes (%d)", task, p, s.maxBytes))
			continue
		} else if err != nil {
			return fmt.Errorf("stage artifact %s of %s: %w", p, task, err)
		}
		if s.files[task] == nil {
			s.files[task] = map[string]string{}
			s.meta[task] = map[string]record.Artifact{}
		}
		s.files[task][p] = dst
		s.meta[task][p] = a
	}
	return nil
}

// locate returns the file for the repository-relative path p, refusing
// anything that is not a regular file inside the repository (symlinks
// included), since the content is published with the record.
func (s *artifactStage) locate(p string) (string, error) {
	full := filepath.Join(s.top, filepath.FromSlash(p))
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("file does not exist")
	} else if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(s.top, parent); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("resolves outside the repository")
	}
	return full, nil
}

var errTooLarge = errors.New("artifact too large")

func (s *artifactStage) copy(src, dst string) (record.Artifact, error) {
	in, err := os.Open(src) //nolint:gosec // an artifact path from uki.yaml, checked by locate
	if err != nil {
		return record.Artifact{}, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is a name uki generated inside its own staging directory
	if err != nil {
		return record.Artifact{}, err
	}
	h := sha256.New()
	// Read one byte past the limit to tell "exactly at the limit" from "over".
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, s.maxBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > s.maxBytes {
		err = errTooLarge
	}
	if err != nil {
		_ = os.Remove(dst)
		return record.Artifact{}, err
	}
	return record.Artifact{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// artifactCmd handles `uki artifact get`.
func (a *app) artifactCmd(args []string) error {
	fs := a.flagSet("artifact")
	rev := fs.String("rev", "HEAD", "commit whose record holds the artifact (a full SHA need not exist locally)")
	output := fs.String("o", "", "write the artifact to this file instead of stdout")
	noFetch := fs.Bool("no-fetch", false, "use the local copy of the records branch instead of fetching")
	fs.Usage = func() {
		fmt.Fprint(a.stderr, "Usage: uki artifact get [flags] <task> <path>\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if len(args) == 0 || args[0] != "get" {
		fs.Usage()
		return errUsage
	}
	if err := a.parse(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return errUsage
	}
	task, path := fs.Arg(0), fs.Arg(1)
	if !config.ValidArtifactTaskName(task) || !config.ValidArtifactPath(path) {
		return fmt.Errorf("no artifact %s of task %q can exist", path, task)
	}

	top, err := gitx.Git{}.TopLevel()
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	g := gitx.Git{Dir: top}
	cfg, err := config.Load(filepath.Join(top, config.FileName))
	if err != nil {
		return err
	}
	sha := *rev
	if !record.ValidSHA(sha) {
		if sha, err = g.ResolveCommit(*rev); err != nil {
			return fmt.Errorf("resolve %s: %w", *rev, err)
		}
	}

	m := marker{g: g, cfg: cfg}
	if cfg.Record.AllowedSigners != "" {
		m.signersFile = filepath.Join(top, cfg.Record.AllowedSigners)
		if _, err := os.Stat(m.signersFile); err != nil {
			return fmt.Errorf("allowed_signers: %w", err)
		}
	}
	if m.recordsRef, err = recordsRef(g, cfg, *noFetch); err != nil {
		return err
	}
	if m.recordsRef == "" {
		return fmt.Errorf("records branch %s not found", cfg.Record.Branch)
	}

	b, err := m.artifact(sha, task, path)
	if err != nil {
		return err
	}
	if *output == "" {
		_, err = a.stdout.Write(b)
		return err
	}
	if dir := filepath.Dir(*output); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return os.WriteFile(*output, b, 0o600)
}

// artifact reads one artifact of sha from the records branch and checks it
// against the digest in the record (whose signature is verified when
// allowed signers are configured).
func (m marker) artifact(sha, task, path string) ([]byte, error) {
	recPath := record.Path(sha)
	rb, err := m.g.Show(m.recordsRef, recPath)
	if errors.Is(err, gitx.ErrNotFound) {
		return nil, fmt.Errorf("no uki record for %s", short(sha))
	} else if err != nil {
		return nil, err
	}
	rec, err := record.Parse(rb)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", recPath, err)
	}
	if m.signersFile != "" {
		if err := m.verifySignature(recPath); err != nil {
			return nil, fmt.Errorf("record signature: %w", err)
		}
	}
	res, ok := rec.Tasks[task]
	if !ok {
		return nil, fmt.Errorf("the record of %s has no task %q", short(sha), task)
	}
	want, ok := res.Artifacts[path]
	if !ok {
		return nil, fmt.Errorf("task %q of %s stored no artifact %s", task, short(sha), path)
	}
	// A reused result points at the artifacts of the run it came from.
	from := sha
	if res.ReusedFrom != "" {
		if !record.ValidSHA(res.ReusedFrom) {
			return nil, fmt.Errorf("invalid reused_from %q", res.ReusedFrom)
		}
		from = res.ReusedFrom
	}
	b, err := m.g.Show(m.recordsRef, record.ArtifactPath(from, task, path))
	if errors.Is(err, gitx.ErrNotFound) {
		return nil, fmt.Errorf("artifact %s of task %q is listed in the record but missing from the records branch", path, task)
	} else if err != nil {
		return nil, err
	}
	if int64(len(b)) != want.Size || sha256Hex(b) != want.SHA256 {
		return nil, fmt.Errorf("artifact %s of task %q does not match the digest in the record", path, task)
	}
	return b, nil
}

// recordsRef fetches the records branch (or, with noFetch, uses the local
// remote-tracking copy) and returns its ref; "" when there is none.
func recordsRef(g gitx.Git, cfg *config.Config, noFetch bool) (string, error) {
	if noFetch {
		ref := "refs/remotes/" + cfg.Record.Remote + "/" + cfg.Record.Branch
		if _, err := g.ResolveCommit(ref); err != nil {
			return "", nil
		}
		return ref, nil
	}
	exists, err := g.RemoteBranchExists(cfg.Record.Remote, cfg.Record.Branch)
	if err != nil || !exists {
		return "", err
	}
	return g.FetchBranch(cfg.Record.Remote, cfg.Record.Branch)
}
