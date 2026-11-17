package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/github"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/inputs"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/verify"
)

// markCmd is meant to run in GitHub Actions with the default branch checked
// out: uki.yaml and the allowed signers file are read from that checkout, so
// a work branch cannot loosen its own requirements.
func (a *app) markCmd(args []string) error {
	fs := a.flagSet("mark")
	sha := fs.String("sha", "", "commit to mark (work-branch push)")
	branch := fs.String("branch", "", "branch the commit was pushed to (work-branch push)")
	before := fs.String("records-before", "", "records branch commit before the push (records push)")
	after := fs.String("records-after", "", "records branch commit after the push (records push)")
	fromEvent := fs.Bool("github-event", false, "take the target from the GitHub Actions push event (GITHUB_REF, GITHUB_SHA, GITHUB_EVENT_PATH)")
	dryRun := fs.Bool("dry-run", false, "print verdicts without calling the GitHub API")
	output := fs.String("output", "text", `verdict format on stdout: "text", or "json" (an array of {sha, branch, state, description}, printed once at the end)`)
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("--output must be text or json, got %q", *output)
	}
	if *fromEvent && (*sha != "" || *branch != "" || *before != "" || *after != "") {
		return errors.New("--github-event cannot be combined with --sha, --branch or --records-*")
	}
	if !*fromEvent && (*sha == "") == (*after == "") {
		return errors.New("pass --github-event, --sha/--branch, or --records-after")
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

	if *fromEvent {
		ev, err := readPushEvent()
		if err != nil {
			return err
		}
		if ev.branch == "" {
			fmt.Fprintf(a.stderr, "uki: %s is not a branch; nothing to mark\n", ev.ref)
			return nil
		}
		// The records branch name comes from uki.yaml, so the workflow does
		// not need to know it.
		if ev.branch == cfg.Record.Branch {
			*before, *after = ev.before, ev.after
		} else {
			*sha, *branch = ev.after, ev.branch
		}
	}

	m := marker{g: g, cfg: cfg, dryRun: *dryRun, out: a.stdout, errOut: a.stderr}
	if *output == "json" {
		verdicts := []markedCommit{}
		m.verdicts = &verdicts
		m.out = io.Discard
		defer func() {
			b, _ := json.Marshal(verdicts)
			fmt.Fprintln(a.stdout, string(b))
		}()
	}
	if cfg.Record.AllowedSigners != "" {
		m.signersFile = filepath.Join(top, cfg.Record.AllowedSigners)
		if _, err := os.Stat(m.signersFile); err != nil {
			return fmt.Errorf("allowed_signers: %w", err)
		}
	}
	if !*dryRun {
		m.client = github.Client{
			Token:  os.Getenv("GITHUB_TOKEN"),
			Repo:   os.Getenv("GITHUB_REPOSITORY"),
			APIURL: os.Getenv("GITHUB_API_URL"),
		}
		if m.client.Token == "" || m.client.Repo == "" {
			return errors.New("GITHUB_TOKEN and GITHUB_REPOSITORY must be set (or use --dry-run)")
		}
		if s, r, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"); s != "" && id != "" {
			m.targetURL = fmt.Sprintf("%s/%s/actions/runs/%s", s, r, id)
		}
	}

	exists, err := g.RemoteBranchExists(cfg.Record.Remote, cfg.Record.Branch)
	if err != nil {
		return err
	}
	if exists {
		if m.recordsRef, err = g.FetchBranch(cfg.Record.Remote, cfg.Record.Branch); err != nil {
			return err
		}
	}

	ctx := context.Background()
	if *sha != "" {
		if !record.ValidSHA(*sha) {
			return fmt.Errorf("--sha must be a full commit SHA, got %q", *sha)
		}
		return m.mark(ctx, *sha, *branch)
	}

	if m.recordsRef == "" {
		return fmt.Errorf("records branch %s does not exist on %s", cfg.Record.Branch, cfg.Record.Remote)
	}
	paths, err := changedRecords(g, *before, *after)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range paths {
		target := strings.TrimSuffix(filepath.Base(p), ".json")
		// Anyone who can push the records branch controls these names.
		if !record.ValidSHA(target) || p != record.Path(target) {
			fmt.Fprintf(a.stderr, "uki: ignoring unexpected file %q on the records branch\n", p)
			continue
		}
		b, err := g.Show(m.recordsRef, p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		rec, err := record.Parse(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		if err := m.mark(ctx, target, latestBranch(rec)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", target, err))
		}
	}
	return errors.Join(errs...)
}

type marker struct {
	g           gitx.Git
	cfg         *config.Config
	recordsRef  string // "" when the records branch does not exist
	signersFile string
	client      github.Client
	targetURL   string
	dryRun      bool
	out         io.Writer
	errOut      io.Writer
	// verdicts, when set, collects every judged commit for --output json.
	verdicts *[]markedCommit
}

// markedCommit is one element of `uki mark --output json`.
type markedCommit struct {
	SHA         string `json:"sha"`
	Branch      string `json:"branch"`
	State       string `json:"state"`
	Description string `json:"description"`
}

func (m marker) mark(ctx context.Context, sha, branch string) error {
	if !m.g.HasCommit(sha) {
		if _, err := m.g.Run("fetch", "--quiet", "--no-tags", "--depth=1", m.cfg.Record.Remote, sha); err != nil {
			fmt.Fprintf(m.errOut, "uki: %s is not on the remote yet; skipping (the branch push will mark it)\n", short(sha))
			return nil
		}
	}
	v, _, err := m.evaluate(sha, branch)
	if err != nil {
		return err
	}
	fmt.Fprintf(m.out, "%s %s %-7s %s\n", short(sha), branch, v.State, v.Description)
	if m.verdicts != nil {
		*m.verdicts = append(*m.verdicts, markedCommit{SHA: sha, Branch: branch, State: v.State, Description: v.Description})
	}
	if m.dryRun {
		return nil
	}

	err = m.client.SetStatus(ctx, sha, github.Status{
		State:       v.State,
		Context:     m.cfg.Status.Context,
		Description: v.Description,
		TargetURL:   m.targetURL,
	})
	if errors.Is(err, github.ErrUnknownCommit) {
		fmt.Fprintf(m.errOut, "uki: GitHub does not know %s yet; skipping\n", short(sha))
		return nil
	}
	return err
}

// evaluate decides the status of sha (which must exist locally) on branch.
// The returned record is nil when none exists.
func (m marker) evaluate(sha, branch string) (verify.Verdict, *record.Record, error) {
	tree, err := m.g.TreeOf(sha)
	if err != nil {
		return verify.Verdict{}, nil, err
	}
	var cfgHash string
	if b, err := m.g.Show(sha, config.FileName); err == nil {
		cfgHash = sha256Hex(b)
	}

	var rec *record.Record
	var sigErr error
	if m.recordsRef != "" {
		path := record.Path(sha)
		b, err := m.g.Show(m.recordsRef, path)
		switch {
		case errors.Is(err, gitx.ErrNotFound):
		case err != nil:
			return verify.Verdict{}, nil, err
		default:
			if rec, err = record.Parse(b); err != nil {
				return verify.Verdict{}, nil, fmt.Errorf("parse %s: %w", path, err)
			}
			if m.signersFile != "" {
				sigErr = m.verifySignature(path)
			}
		}
	}
	if sigErr != nil {
		fmt.Fprintf(m.errOut, "uki: signature check failed: %v\n", sigErr)
	}

	required, matched := m.cfg.TasksForBranch(branch)
	var invalidReuse map[string]bool
	if rec != nil && sigErr == nil && rec.SHA == sha {
		trees := map[string]*inputs.Tree{}
		for _, name := range required {
			res, ok := rec.Tasks[name]
			if !ok || res.ReusedFrom == "" {
				continue
			}
			if err := m.checkReuse(sha, cfgHash, name, res, trees); err != nil {
				fmt.Fprintf(m.errOut, "uki: [%s] reused result is not valid: %v\n", name, err)
				if invalidReuse == nil {
					invalidReuse = map[string]bool{}
				}
				invalidReuse[name] = true
			}
		}
	}
	return verify.Evaluate(verify.Input{
		SHA:            sha,
		Tree:           tree,
		ConfigSHA256:   cfgHash,
		Required:       required,
		NoMatchingRule: !matched,
		Record:         rec,
		SignatureErr:   sigErr,
		InvalidReuse:   invalidReuse,
	}), rec, nil
}

// verifySignature checks the latest records-branch commit that touched path.
// Only SSH signatures are supported (gpg.ssh.allowedSignersFile).
func (m marker) verifySignature(path string) error {
	c, err := m.g.Run("log", "-1", "--format=%H", m.recordsRef, "--", path)
	if err != nil {
		return err
	}
	if c == "" {
		return errors.New("no commit touches " + path)
	}
	// verify-commit also accepts GPG signatures from any key in the local
	// keyring (exit 0 even with unknown trust), so insist on SSH signatures,
	// which are checked against the allowed signers file only.
	raw, err := m.g.Run("cat-file", "commit", c)
	if err != nil {
		return err
	}
	if !strings.Contains(raw, "\ngpgsig -----BEGIN SSH SIGNATURE-----") {
		return fmt.Errorf("commit %s is not SSH-signed", short(c))
	}
	out, err := m.g.Run("-c", "gpg.ssh.allowedSignersFile="+m.signersFile,
		"log", "-1", "--format=%G?", c)
	if err != nil {
		return err
	}
	if out != "G" {
		return fmt.Errorf("commit %s: signature status %q (want G)", short(c), out)
	}
	return nil
}

// changedRecords lists record files added or modified between before and after.
func changedRecords(g gitx.Git, before, after string) ([]string, error) {
	var out string
	var err error
	if before == "" || strings.Trim(before, "0") == "" || !g.HasCommit(before) {
		out, err = g.Run("ls-tree", "-r", "--name-only", after, "--", "records/")
	} else {
		out, err = g.Run("diff", "--name-only", "--diff-filter=AM", before, after, "--", "records/")
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l, ".json") {
			paths = append(paths, l)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// latestBranch picks the branch of the most recently started task, which is
// the best guess for which branch rules apply on a records push.
func latestBranch(r *record.Record) string {
	var b string
	var latest int64
	first := true
	for _, t := range r.Tasks {
		if ts := t.StartedAt.UnixNano(); first || ts > latest {
			b, latest, first = t.Branch, ts, false
		}
	}
	return b
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

type pushEvent struct {
	ref, branch   string
	before, after string
}

// readPushEvent reads the push being handled from the GitHub Actions
// environment. branch is empty when the ref is not a branch (e.g. a tag).
func readPushEvent() (pushEvent, error) {
	if name := os.Getenv("GITHUB_EVENT_NAME"); name != "" && name != "push" {
		return pushEvent{}, fmt.Errorf("--github-event supports push events only, got %q", name)
	}
	ev := pushEvent{ref: os.Getenv("GITHUB_REF"), after: os.Getenv("GITHUB_SHA")}
	if ev.ref == "" || ev.after == "" {
		return pushEvent{}, errors.New("--github-event needs GITHUB_REF and GITHUB_SHA")
	}
	ev.branch, _ = strings.CutPrefix(ev.ref, "refs/heads/")
	if ev.branch == ev.ref {
		ev.branch = ""
	}
	if path := os.Getenv("GITHUB_EVENT_PATH"); path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // path is provided by the GitHub Actions runner
		if err != nil {
			return pushEvent{}, fmt.Errorf("read push event: %w", err)
		}
		var payload struct {
			Before string `json:"before"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			return pushEvent{}, fmt.Errorf("parse push event: %w", err)
		}
		ev.before = payload.Before
	}
	return ev, nil
}
