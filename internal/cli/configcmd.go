package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
)

// configKeys are the settings `uki config get` can print, with defaults applied.
var configKeys = map[string]func(*config.Config) string{
	"record.branch":          func(c *config.Config) string { return c.Record.Branch },
	"record.remote":          func(c *config.Config) string { return c.Record.Remote },
	"record.sign":            func(c *config.Config) string { return strconv.FormatBool(c.Record.Sign) },
	"record.allowed_signers": func(c *config.Config) string { return c.Record.AllowedSigners },
	"status.context":         func(c *config.Config) string { return c.Status.Context },
}

func configKeyNames() string {
	names := make([]string, 0, len(configKeys))
	for k := range configKeys {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// configCmd prints settings from the working tree's uki.yaml, for scripts
// such as git hooks.
func (a *app) configCmd(args []string) error {
	fs := a.flagSet("config")
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage: uki config get <key>\n\nKeys: %s\n", configKeyNames())
	}
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 || fs.Arg(0) != "get" {
		fs.Usage()
		return errUsage
	}
	get, ok := configKeys[fs.Arg(1)]
	if !ok {
		return fmt.Errorf("unknown config key %q (known: %s)", fs.Arg(1), configKeyNames())
	}

	top, err := gitx.Git{}.TopLevel()
	if err != nil {
		return errors.New("not in a git repository")
	}
	cfg, err := config.Load(filepath.Join(top, config.FileName))
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, get(cfg))
	return nil
}
