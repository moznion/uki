// Package config loads and interprets uki.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

// FileName is the config file name, looked up at the repository root.
const FileName = "uki.yaml"

type Config struct {
	Tasks    map[string]Task `yaml:"tasks"`
	Branches []BranchRule    `yaml:"branches"`
	Record   RecordConfig    `yaml:"record"`
	Status   StatusConfig    `yaml:"status"`
}

type Task struct {
	Run string            `yaml:"run"`
	Env map[string]string `yaml:"env"`
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
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	for name, t := range c.Tasks {
		if t.Run == "" {
			return fmt.Errorf("task %q: run is empty", name)
		}
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

// TasksForBranch returns the tasks of the first rule matching branch.
func (c *Config) TasksForBranch(branch string) ([]string, bool) {
	for _, r := range c.Branches {
		if matchGlob(r.Match, branch) {
			return r.Tasks, true
		}
	}
	return nil, false
}
