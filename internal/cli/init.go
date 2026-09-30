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
			cfg := config.Default()
			cfg.Name = filepath.Base(cwd)

			if !yes && isTTY() {
				if err := runInitForm(cfg); err != nil {
					return err
				}
			}
			if err := os.WriteFile(config.FileName, cfg.Render(), 0o644); err != nil {
				return err
			}

			printBox("llok initialised", kv([][2]string{
				{"project", cfg.Name},
				{"agent", cfg.Agent},
				{"vm", fmt.Sprintf("%d vCPU · %d MiB", cfg.VM.CPUs, cfg.VM.MemoryMB)},
				{"repo", cwd + "  →  /repo (rw)"},
				{"network", "none (only the llok broker, later)"},
				{"policy", config.FileName},
			}))
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, styleDim.Render("next:")+"  llok up          "+styleDim.Render("open a shell inside the VM"))
			fmt.Fprintln(os.Stderr, "       llok up -c CMD   "+styleDim.Render("run one command and power off"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing llok.yaml")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults without prompting")
	return cmd
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
