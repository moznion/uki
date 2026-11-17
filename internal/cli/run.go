package cli

import (
	"bytes"
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
	jobsFlag := fs.Int("jobs", 0, "run up to this many tasks at the same time (default: jobs in uki.yaml, or 1)")
	fs.IntVar(jobsFlag, "j", 0, "shorthand for -jobs")
	if err := a.parse(fs, args); err != nil {
		return 2, err
	}
	if *jobsFlag < 0 {
		fmt.Fprintln(a.stderr, "uki: -jobs must not be negative")
		return 2, errUsage
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
	tasks = dedupe(tasks)
	jobs := cfg.Jobs
	if *jobsFlag > 0 {
		jobs = *jobsFlag
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

	var toRun []string
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
		toRun = append(toRun, name)
	}

	var results []runner.Result
	var failed bool
	if jobs <= 1 {
		results, failed, err = a.runSequential(ctx, top, cfg, toRun, *failFast, stage)
	} else {
		results, failed, err = a.runParallel(ctx, top, cfg, toRun, jobs, *failFast, stage)
	}
	if err != nil {
		return 1, err
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

// dedupe drops repeated task names, keeping the first: a task's result is
// recorded once per commit, so running it twice in one run is pointless.
func dedupe(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// runSequential runs tasks one after another, streaming their output.
func (a *app) runSequential(ctx context.Context, top string, cfg *config.Config, tasks []string, failFast bool, stage *artifactStage) ([]runner.Result, bool, error) {
	var results []runner.Result
	failed := false
	for _, name := range tasks {
		res := runner.Run(ctx, top, name, cfg.Tasks[name], a.stdout, a.stderr)
		results = append(results, res)
		// Collect right away: a later task may overwrite or remove the files.
		if stage != nil && ctx.Err() == nil {
			if err := stage.collect(name, cfg.Tasks[name].Artifacts, a.warn); err != nil {
				return nil, false, err
			}
		}
		if !res.Success {
			failed = true
			if failFast || ctx.Err() != nil {
				break
			}
		}
	}
	return results, failed, nil
}

type finished struct {
	name           string
	res            runner.Result
	stdout, stderr *bytes.Buffer
	cancelledByUs  bool
}

// runParallel runs up to jobs tasks at the same time. Tasks start in the
// listed order as slots free up, except that a task waits while another
// task holding one of its locks runs. Each task's output is buffered and
// printed in one piece when it ends, so outputs never interleave.
//
// With failFast, the first failure stops the tasks still running; their
// results are dropped, as if they had not been started.
func (a *app) runParallel(ctx context.Context, top string, cfg *config.Config, tasks []string, jobs int, failFast bool, stage *artifactStage) ([]runner.Result, bool, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pending := append([]string(nil), tasks...)
	held := map[string]bool{}
	running := 0
	stopping := false
	done := make(chan finished)

	free := func(name string) bool {
		for _, l := range cfg.Tasks[name].Locks {
			if held[l] {
				return false
			}
		}
		return true
	}
	start := func(name string) {
		for _, l := range cfg.Tasks[name].Locks {
			held[l] = true
		}
		running++
		fmt.Fprintf(a.stderr, "==> [%s] started\n", name)
		go func() {
			var out, errOut bytes.Buffer
			res := runner.RunWith(runCtx, top, name, cfg.Tasks[name], &out, &errOut, runner.Options{Detach: true})
			done <- finished{name: name, res: res, stdout: &out, stderr: &errOut, cancelledByUs: runCtx.Err() != nil && ctx.Err() == nil}
		}()
	}

	var results []runner.Result
	failed := false
	var collectErr error
	for {
		if !stopping {
			rest := pending[:0]
			for _, name := range pending {
				if running < jobs && free(name) {
					start(name)
				} else {
					rest = append(rest, name)
				}
			}
			pending = rest
		}
		if running == 0 {
			break
		}

		f := <-done
		running--
		for _, l := range cfg.Tasks[f.name].Locks {
			held[l] = false
		}
		_, _ = a.stdout.Write(f.stdout.Bytes())
		_, _ = a.stderr.Write(f.stderr.Bytes())
		if f.cancelledByUs && !f.res.Success {
			fmt.Fprintf(a.stderr, "==> [%s] stopped after another task failed (not recorded)\n", f.name)
			continue
		}
		results = append(results, f.res)
		if stage != nil && ctx.Err() == nil && collectErr == nil {
			collectErr = stage.collect(f.name, cfg.Tasks[f.name].Artifacts, a.warn)
		}
		if collectErr != nil && !stopping {
			stopping = true
			cancel()
		}
		if !f.res.Success {
			failed = true
			if failFast && !stopping {
				stopping = true
				cancel()
			}
		}
		if ctx.Err() != nil {
			stopping = true
		}
	}
	if collectErr != nil {
		return nil, false, collectErr
	}
	return results, failed, nil
}
