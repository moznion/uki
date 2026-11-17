// Package config loads and interprets uki.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the config file name, looked up at the repository root.
const FileName = "uki.yaml"

// DefaultMaxArtifactBytes is the default per-file limit for task artifacts.
// It stays well below GitHub's 100 MB per-file push limit.
const DefaultMaxArtifactBytes = 10 << 20

type Config struct {
	// Inputs are file patterns added to the inputs of every task that
	// declares its own (e.g. tool versions shared by all tasks).
	Inputs   []string        `yaml:"inputs"`
	Tasks    map[string]Task `yaml:"tasks"`
	Branches []BranchRule    `yaml:"branches"`
	Record   RecordConfig    `yaml:"record"`
	Status   StatusConfig    `yaml:"status"`
}

type Task struct {
	Run string            `yaml:"run"`
	Env map[string]string `yaml:"env"`
	// Artifacts are repository-relative files the task produces (e.g. a
	// coverage profile). They are stored on the records branch next to the
	// record, so they must be ignored by git rather than tracked.
	Artifacts []string `yaml:"artifacts"`
	// Inputs are the file patterns the task's result depends on. When set,
	// `uki run` reuses a passing result recorded for the same inputs instead
	// of running the task again. Without it the task always runs.
	Inputs []string `yaml:"inputs"`
}

// BranchRule maps a branch glob to the tasks required on matching branches.
// `*` and `?` do not cross `/`; `**` matches anything.
type BranchRule struct {
	Match string   `yaml:"match"`
	Tasks []string `yaml:"tasks"`
}

type RecordConfig struct {
	// Branch is the orphan branch that holds the records.
	Branch string `yaml:"branch"`
	// Remote is the git remote that records are pushed to.
	Remote string `yaml:"remote"`
	// Sign signs record commits with `git commit -S`.
	Sign bool `yaml:"sign"`
	// AllowedSigners is a repo-relative path to an SSH allowed_signers file.
	// When set, `uki mark` rejects records whose commit signature does not verify.
	AllowedSigners string `yaml:"allowed_signers"`
	// MaxArtifactBytes caps the size of each artifact file. Larger files are
	// left out of the record with a warning. 0 means DefaultMaxArtifactBytes.
	MaxArtifactBytes int64 `yaml:"max_artifact_bytes"`
}

type StatusConfig struct {
	// Context is the commit status context name shown on GitHub.
	Context string `yaml:"context"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // reading the user's own config file is the point
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	// Reject unknown keys so typos like `taks:` fail loudly instead of
	// being silently ignored.
	dec.KnownFields(true)
	// An empty document yields io.EOF; treat it like yaml.Unmarshal does.
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if c.Record.Branch == "" {
		c.Record.Branch = "uki/records"
	}
	if c.Record.Remote == "" {
		c.Record.Remote = "origin"
	}
	if c.Status.Context == "" {
		c.Status.Context = "uki"
	}
	if c.Record.MaxArtifactBytes == 0 {
		c.Record.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	for _, p := range c.Inputs {
		if !validInputPattern(p) {
			return fmt.Errorf("inputs: pattern %q must be a clean repository-relative path or glob", p)
		}
	}
	for name, t := range c.Tasks {
		for _, p := range t.Inputs {
			if !validInputPattern(p) {
				return fmt.Errorf("task %q: input pattern %q must be a clean repository-relative path or glob", name, p)
			}
		}
		if t.Run == "" {
			return fmt.Errorf("task %q: run is empty", name)
		}
		if len(t.Artifacts) > 0 && !ValidArtifactTaskName(name) {
			return fmt.Errorf("task %q: a task with artifacts must have a name usable as a directory (no \"/\", not \".\" or \"..\")", name)
		}
		seen := map[string]bool{}
		for _, a := range t.Artifacts {
			if !ValidArtifactPath(a) {
				return fmt.Errorf("task %q: artifact %q must be a clean repository-relative path to a file", name, a)
			}
			if seen[a] {
				return fmt.Errorf("task %q: artifact %q is listed twice", name, a)
			}
			seen[a] = true
		}
	}
	if c.Record.MaxArtifactBytes < 0 {
		return errors.New("record.max_artifact_bytes must not be negative")
	}
	for i, r := range c.Branches {
		if r.Match == "" {
			return fmt.Errorf("branches[%d]: match is empty", i)
		}
		for _, t := range r.Tasks {
			if _, ok := c.Tasks[t]; !ok {
				return fmt.Errorf("branches[%d] (%s): unknown task %q", i, r.Match, t)
			}
		}
	}
	return nil
}

// TaskInputs returns the effective input patterns of task: the top-level
// inputs followed by the task's own, or nil when the task declares none
// (its result is then never reused).
func (c *Config) TaskInputs(task string) []string {
	t, ok := c.Tasks[task]
	if !ok || len(t.Inputs) == 0 {
		return nil
	}
	return append(append([]string{}, c.Inputs...), t.Inputs...)
}

// InputMatcher returns a function reporting whether a repository-relative
// path matches any of patterns (branch glob syntax: `*` and `?` do not cross
// `/`, `**` does). The patterns are compiled once.
func InputMatcher(patterns []string) func(string) bool {
	res := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		res[i] = globToRegexp(p)
	}
	return func(path string) bool {
		for _, re := range res {
			if re.MatchString(path) {
				return true
			}
		}
		return false
	}
}

func validInputPattern(p string) bool {
	return ValidArtifactPath(p)
}

// TasksForBranch returns the tasks of the first rule matching branch.
func (c *Config) TasksForBranch(branch string) ([]string, bool) {
	for _, r := range c.Branches {
		if matchGlob(r.Match, branch) {
			return r.Tasks, true
		}
	}
	return nil, false
}

// ValidArtifactPath reports whether p is a clean, slash-separated path that
// stays inside the repository: not absolute, no "." or ".." elements, no
// backslashes, and not inside .git.
func ValidArtifactPath(p string) bool {
	if p == "" || p != path.Clean(p) || path.IsAbs(p) || strings.Contains(p, "\\") {
		return false
	}
	for _, e := range strings.Split(p, "/") {
		if e == "." || e == ".." || e == ".git" {
			return false
		}
	}
	return true
}

// ValidArtifactTaskName reports whether name can be used as one directory
// level in artifact paths on the records branch.
func ValidArtifactTaskName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}
