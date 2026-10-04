package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/config"
)

func newInitCmd() *cobra.Command {
	var (
		force bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up llok for this repo (writes llok.yaml)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := os.Stat(config.FileName); err == nil && !force {
				return errors.New(config.FileName + " already exists (use --force to overwrite)")
			}
			cwd, _ := os.Getwd()
			if err := checkRepoDir(cwd); err != nil {
				return err
			}
			_, err := setupRepo(cwd, !yes && isTTY())
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing llok.yaml")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults without prompting")
	return cmd
}

// setupRepo creates llok.yaml in dir, asking the setup questions when
// interactive, and prints the summary. It is used by `llok init` and by
// `llok` itself when it finds a repo without a policy file.
func setupRepo(dir string, interactive bool) (*config.Config, error) {
	cfg := config.Default()
	cfg.Name = filepath.Base(dir)
	if interactive {
		if err := runInitForm(cfg); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, cfg.Render(), 0o644); err != nil {
		return nil, err
	}
	printBox("llok initialised", kv([][2]string{
		{"project", cfg.Name},
		{"agent", cfg.Agent},
		{"vm", fmt.Sprintf("%d vCPU · %d MiB", cfg.VM.CPUs, cfg.VM.MemoryMB)},
		{"repo", dir + "  →  " + cfg.Mount.Repo + " (rw)"},
		{"network", cfg.Network.Mode + "  →  api.anthropic.com only, plus network.allow"},
		{"policy", path},
	}))
	fmt.Fprintln(os.Stderr)
	return cfg, nil
}

func runInitForm(cfg *config.Config) error {
	var err error
	if cfg.Name, err = inputPrompt("Project name", "Shown in the VM prompt and logs.", cfg.Name); err != nil {
		return err
	}
	if cfg.Agent, err = selectPrompt("Agent", "Which coding agent will run inside the VM.", []option[string]{
		{"Claude Code", "claude-code"},
		{"Shell only (no agent yet)", "shell"},
	}, 0); err != nil {
		return err
	}
	if cfg.VM.CPUs, err = selectPrompt("vCPUs", "", []option[uint]{
		{"1", 1}, {"2  (default)", 2}, {"4", 4}, {"8", 8},
	}, 1); err != nil {
		return err
	}
	if cfg.VM.MemoryMB, err = selectPrompt("Memory", "", []option[uint64]{
		{"512 MiB", 512}, {"1 GiB  (default)", 1024}, {"2 GiB", 2048}, {"4 GiB", 4096},
	}, 1); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr)
	return nil
}
