package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/inputs"
	"github.com/moznion/uki/internal/record"
)

// reuser computes inputs keys for HEAD and finds passing results recorded
// for the same inputs.
type reuser struct {
	g          gitx.Git
	cfg        *config.Config
	sha        string
	cfgHash    string
	tree       *inputs.Tree
	recordsRef string // "" disables lookups (nothing to reuse from)
}

// newReuser prepares keys for sha. With lookup, it also fetches the records
// branch so that earlier results can be found.
func newReuser(g gitx.Git, cfg *config.Config, sha string, lookup bool) (*reuser, error) {
	cfgBytes, err := g.Show(sha, config.FileName)
	if err != nil {
		return nil, fmt.Errorf("read committed %s: %w", config.FileName, err)
	}
	tree, err := inputs.ReadTree(g, sha)
	if err != nil {
		return nil, err
	}
	r := &reuser{g: g, cfg: cfg, sha: sha, cfgHash: sha256Hex(cfgBytes), tree: tree}
	if lookup {
		if r.recordsRef, err = recordsRef(g, cfg, false); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// key is the inputs key of task at HEAD, or "" when it declares no inputs.
func (r *reuser) key(task string) string {
	patterns := r.cfg.TaskInputs(task)
	if patterns == nil {
		return ""
	}
	return r.tree.Key(r.cfgHash, task, patterns)
}

// find returns the result to record for task when a passing, actually-run
// result with the same inputs key exists for another commit.
func (r *reuser) find(task, branch string) (record.TaskResult, bool, error) {
	key := r.key(task)
	if key == "" || r.recordsRef == "" {
		return record.TaskResult{}, false, nil
	}
	b, err := r.g.Show(r.recordsRef, record.IndexPath(key))
	if errors.Is(err, gitx.ErrNotFound) {
		return record.TaskResult{}, false, nil
	} else if err != nil {
		return record.TaskResult{}, false, err
	}
	var e record.IndexEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return record.TaskResult{}, false, fmt.Errorf("parse %s: %w", record.IndexPath(key), err)
	}
	// The same commit: the user is re-running it on purpose.
	if !record.ValidSHA(e.SHA) || e.Task != task || e.SHA == r.sha {
		return record.TaskResult{}, false, nil
	}
	ob, err := r.g.Show(r.recordsRef, record.Path(e.SHA))
	if err != nil {
		return record.TaskResult{}, false, nil //nolint:nilerr // a dangling index entry only means nothing to reuse
	}
	origin, err := record.Parse(ob)
	if err != nil {
		return record.TaskResult{}, false, nil //nolint:nilerr // see above
	}
	res, ok := origin.Tasks[task]
	if !ok || !res.Success || res.ReusedFrom != "" || res.InputsSHA256 != key || res.ConfigSHA256 != r.cfgHash {
		return record.TaskResult{}, false, nil
	}
	res.Branch = branch
	res.StartedAt = time.Now().UTC()
	res.DurationMS = 0
	res.ReusedFrom = e.SHA
	return res, true, nil
}

// checkReuse verifies the reused result of task in the record of sha with
// the rules of the checked-out (default branch) uki.yaml: the task must
// declare inputs, and the files they match must be identical at sha and at
// the commit the result came from, whose record must show a real, passing
// run.
func (m marker) checkReuse(sha, cfgHash, task string, res record.TaskResult, trees map[string]*inputs.Tree) error {
	patterns := m.cfg.TaskInputs(task)
	if patterns == nil {
		return errors.New("the task declares no inputs, so its result cannot be reused")
	}
	origin := res.ReusedFrom
	if !record.ValidSHA(origin) {
		return fmt.Errorf("invalid reused_from %q", origin)
	}
	path := record.Path(origin)
	b, err := m.g.Show(m.recordsRef, path)
	if err != nil {
		return fmt.Errorf("no record for %s", short(origin))
	}
	orec, err := record.Parse(b)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	ores, ok := orec.Tasks[task]
	if !ok || !ores.Success || ores.ReusedFrom != "" || orec.SHA != origin {
		return fmt.Errorf("%s has no passing run of the task", short(origin))
	}
	if m.signersFile != "" {
		if err := m.verifySignature(path); err != nil {
			return fmt.Errorf("record of %s: %w", short(origin), err)
		}
	}
	if !m.g.HasCommit(origin) {
		if _, err := m.g.Run("fetch", "--quiet", "--no-tags", "--depth=1", m.cfg.Record.Remote, origin); err != nil {
			return fmt.Errorf("fetch %s: %w", short(origin), err)
		}
	}
	ocfg, err := m.g.Show(origin, config.FileName)
	if err != nil {
		return fmt.Errorf("read %s at %s: %w", config.FileName, short(origin), err)
	}
	tree := func(rev string) (*inputs.Tree, error) {
		if t, ok := trees[rev]; ok {
			return t, nil
		}
		t, err := inputs.ReadTree(m.g, rev)
		if err == nil {
			trees[rev] = t
		}
		return t, err
	}
	cur, err := tree(sha)
	if err != nil {
		return err
	}
	prev, err := tree(origin)
	if err != nil {
		return err
	}
	if cur.Key(cfgHash, task, patterns) != prev.Key(sha256Hex(ocfg), task, patterns) {
		return fmt.Errorf("inputs differ from %s", short(origin))
	}
	return nil
}
