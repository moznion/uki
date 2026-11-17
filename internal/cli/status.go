package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/verify"
)

// Exit codes of `uki status`.
const (
	statusExitSuccess = 0
	statusExitFailed  = 1 // failure or error
	statusExitPending = 3 // no record yet, or required tasks missing
)

// statusCmd shows the record and verdict for a commit (HEAD by default),
// using the same evaluation as `uki mark`.
func (a *app) statusCmd(args []string) (int, error) {
	fs := a.flagSet("status")
	rev := fs.String("rev", "HEAD", "commit to inspect")
	branchFlag := fs.String("branch", "", "branch name used for rule matching (default: current branch)")
	noFetch := fs.Bool("no-fetch", false, "use the local copy of the records branch instead of fetching")
	if err := a.parse(fs, args); err != nil {
		return 2, err
	}

	top, err := gitx.Git{}.TopLevel()
	if err != nil {
		return 1, fmt.Errorf("not in a git repository: %w", err)
	}
	g := gitx.Git{Dir: top}
	cfg, err := config.Load(filepath.Join(top, config.FileName))
	if err != nil {
		return 1, err
	}

	sha, err := g.ResolveCommit(*rev)
	if err != nil {
		return 1, fmt.Errorf("resolve %s: %w", *rev, err)
	}
	branch := *branchFlag
	if branch == "" {
		if branch, err = g.CurrentBranch(); err != nil {
			return 1, err
		}
	}

	m := marker{g: g, cfg: cfg, dryRun: true, out: a.stdout, errOut: a.stderr}
	if cfg.Record.AllowedSigners != "" {
		m.signersFile = filepath.Join(top, cfg.Record.AllowedSigners)
		if _, err := os.Stat(m.signersFile); err != nil {
			return 1, fmt.Errorf("allowed_signers: %w", err)
		}
	}
	if m.recordsRef, err = recordsRef(g, cfg, *noFetch); err != nil {
		return 1, err
	}

	v, rec, err := m.evaluate(sha, branch)
	if err != nil {
		return 1, err
	}

	branchLabel := branch
	if branchLabel == "" {
		branchLabel = "detached"
	}
	fmt.Fprintf(a.stdout, "commit  %s (%s)\n", short(sha), branchLabel)
	if rec == nil {
		fmt.Fprintln(a.stdout, "record  none")
	} else {
		fmt.Fprintf(a.stdout, "record  %s:%s\n", strings.TrimPrefix(m.recordsRef, "refs/remotes/"), record.Path(sha))
		printTasks(a.stdout, rec)
	}
	fmt.Fprintf(a.stdout, "status  %s: %s\n", v.State, v.Description)

	if head, err := g.ResolveCommit("HEAD"); err == nil && head == sha {
		if dirty, err := g.IsDirty(); err == nil && dirty {
			a.warn("working tree is dirty; this status does not cover local changes")
		}
	}
	a.warnIfRulesDiffer(g, cfg)

	switch v.State {
	case verify.StateSuccess:
		return statusExitSuccess, nil
	case verify.StatePending:
		return statusExitPending, nil
	default:
		return statusExitFailed, nil
	}
}

func printTasks(w io.Writer, rec *record.Record) {
	names := make([]string, 0, len(rec.Tasks))
	width := 0
	for n := range rec.Tasks {
		names = append(names, n)
		width = max(width, len(n))
	}
	sort.Strings(names)
	for _, n := range names {
		t := rec.Tasks[n]
		result := "ok"
		if !t.Success {
			result = fmt.Sprintf("FAILED (exit %d)", t.ExitCode)
		}
		fmt.Fprintf(w, "  %-*s  %-16s %8s  %s  %s/%s\n", width, n, result,
			(time.Duration(t.DurationMS) * time.Millisecond).String(),
			t.StartedAt.Local().Format(time.DateTime), t.OS, t.Arch)
		paths := make([]string, 0, len(t.Artifacts))
		for p := range t.Artifacts {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Fprintf(w, "  %-*s  artifact %s (%s)\n", width, "", p, humanBytes(t.Artifacts[p].Size))
		}
	}
}

// warnIfRulesDiffer notes when the local uki.yaml differs from the remote
// default branch's, since GitHub Actions evaluates with the latter.
func (a *app) warnIfRulesDiffer(g gitx.Git, cfg *config.Config) {
	head, err := g.Run("symbolic-ref", "--quiet", "refs/remotes/"+cfg.Record.Remote+"/HEAD")
	if err != nil {
		return
	}
	remote, err := g.Show(head, config.FileName)
	if err != nil {
		return
	}
	local, err := os.ReadFile(filepath.Join(g.Dir, config.FileName))
	if err != nil {
		return
	}
	if sha256Hex(remote) != sha256Hex(local) {
		a.warn(fmt.Sprintf("local %s differs from %s; GitHub evaluates with the latter", config.FileName, head))
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
