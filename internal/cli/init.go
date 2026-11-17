package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/repofile"
)

func (a *app) initCmd(args []string) error {
	fs := a.flagSet("init")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	top, err := gitx.Git{}.TopLevel()
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	files := []struct{ tmpl, dst string }{
		{"templates/uki.yaml", config.FileName},
		{"templates/workflow.yml", WorkflowPath},
	}
	for _, f := range files {
		dst := filepath.Join(top, f.dst)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(a.stderr, "skip   %s (exists)\n", f.dst)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err := templates.ReadFile(f.tmpl)
		if err != nil {
			return err
		}
		if err := repofile.Write(dst, b); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "create %s\n", f.dst)
	}
	return nil
}
