package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/recorder"
	"github.com/moznion/uki/internal/runner"
)

func (a *app) runCmd(args []string) (int, error) {
	fs := a.flagSet("run")
	noRecord := fs.Bool("no-record", false, "run tasks without recording the result")
	failFast := fs.Bool("fail-fast", false, "stop at the first failing task")
	branchFlag := fs.String("branch", "", "branch name used for rule matching (default: current branch)")
	noReuse := fs.Bool("no-reuse", false, "run every task even when a passing result for the same inputs is recorded")
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

	branch := *branchFlag
	if branch == "" {
		if branch, err = g.CurrentBranch(); err != nil {
			return 1, err
		}
	}

	tasks := fs.Args()
	if len(tasks) == 0 {
		if branch == "" {
			return 1, errors.New("detached HEAD: pass --branch or task names")
		}
		var ok bool
		if tasks, ok = cfg.TasksForBranch(branch); !ok {
			return 1, fmt.Errorf("no branch rule matches %q", branch)
		}
	}
	for _, t := range tasks {
		if _, ok := cfg.Tasks[t]; !ok {
			return 1, fmt.Errorf("unknown task %q", t)
		}
	}
	if err := checkArtifactsUntracked(g, cfg, tasks); err != nil {
		return 1, err
	}

	sha, err := g.ResolveCommit("HEAD")
	if err != nil {
		return 1, fmt.Errorf("resolve HEAD (no commits yet?): %w", err)
	}
	dirty, err := g.IsDirty()
	if err != nil {
		return 1, err
	}
	doRecord := !*noRecord
	if dirty && doRecord {
		a.warn("working tree is dirty; tasks will run but the result will NOT be recorded")
		doRecord = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Keys are computed whenever the result is recorded, so a real run indexes
	// its inputs even with --no-reuse. Results are reused only when recording:
	// a --no-record run (e.g. CI that sets statuses itself) always runs.
	var ru *reuser
	if doRecord {
		if ru, err = newReuser(g, cfg, sha, !*noReuse); err != nil {
			return 1, err
		}
	}
	reused := map[string]record.TaskResult{}

	var stage *artifactStage
	if doRecord {
		if stage, err = newArtifactStage(top, cfg.Record.MaxArtifactBytes); err != nil {
			return 1, err
		}
		defer stage.cleanup()
	}

	var results []runner.Result
	failed := false
	for _, name := range tasks {
		if ru != nil {
			r, ok, err := ru.find(name, branch)
			if err != nil {
				return 1, err
			}
			if ok {
				fmt.Fprintf(a.stderr, "==> [%s] reused the passing result of %s (inputs unchanged)\n", name, short(r.ReusedFrom))
				reused[name] = r
				continue
			}
		}
		delete(reused, name)
		res := runner.Run(ctx, top, name, cfg.Tasks[name], a.stdout, a.stderr)
		results = append(results, res)
		// Collect right away: a later task may overwrite or remove the files.
		if stage != nil && ctx.Err() == nil {
			if err := stage.collect(name, cfg.Tasks[name].Artifacts, a.warn); err != nil {
				return 1, err
			}
		}
		if !res.Success {
			failed = true
			if *failFast || ctx.Err() != nil {
				break
			}
		}
	}
	code := 0
	if failed {
		code = 1
	}
	if ctx.Err() != nil {
		a.warn("interrupted; result will NOT be recorded")
		return 130, nil
	}
	if !doRecord {
		return code, nil
	}

	// Guard against HEAD moving or files changing while tasks ran.
	if now, err := g.ResolveCommit("HEAD"); err != nil || now != sha {
		a.warn("HEAD changed while tasks were running; result will NOT be recorded")
		return code, nil
	}
	if d, err := g.IsDirty(); err != nil {
		return 1, err
	} else if d {
		a.warn("tasks left the working tree dirty; result will NOT be recorded (add generated files to .gitignore)")
		return code, nil
	}

	if err := a.save(g, cfg, sha, branch, results, reused, ru, stage); err != nil {
		return 1, fmt.Errorf("record: %w", err)
	}
	fmt.Fprintf(a.stderr, "uki: recorded %s to %s/%s\n", sha[:12], cfg.Record.Remote, cfg.Record.Branch)
	return code, nil
}

func (a *app) save(g gitx.Git, cfg *config.Config, sha, branch string, results []runner.Result, reused map[string]record.TaskResult, ru *reuser, stage *artifactStage) error {
	tree, err := g.TreeOf(sha)
	if err != nil {
		return err
	}
	cfgBytes, err := g.Show(sha, config.FileName)
	if err != nil {
		return fmt.Errorf("read committed %s: %w", config.FileName, err)
	}
	cfgHash := sha256Hex(cfgBytes)

	m := make(map[string]record.TaskResult, len(results)+len(reused))
	for name, r := range reused {
		m[name] = r
	}
	for _, r := range results {
		m[r.Name] = record.TaskResult{
			Command:      r.Command,
			Success:      r.Success,
			ExitCode:     r.ExitCode,
			Branch:       branch,
			ConfigSHA256: cfgHash,
			StartedAt:    r.StartedAt,
			DurationMS:   r.Duration.Milliseconds(),
			LogSHA256:    r.LogSHA256,
			OS:           runtime.GOOS,
			Arch:         runtime.GOARCH,
			UkiVersion:   version(),
			Artifacts:    stage.meta[r.Name],
			InputsSHA256: ru.key(r.Name),
		}
	}
	return recorder.Recorder{
		Git:       g,
		Remote:    cfg.Record.Remote,
		Branch:    cfg.Record.Branch,
		Sign:      cfg.Record.Sign,
		SeedFiles: []string{WorkflowPath, ActionsLockPath},
		Log:       a.stderr,
	}.Save(sha, tree, m, stage.files)
}
