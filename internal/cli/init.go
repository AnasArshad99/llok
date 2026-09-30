package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/muhammadanas/llok/internal/config"
)

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a default llok.yaml policy into the current repo",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.FileName
			if _, err := os.Stat(path); err == nil && !force {
				return errors.New(path + " already exists (use --force to overwrite)")
			}
			if err := os.WriteFile(path, []byte(config.DefaultYAML), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing llok.yaml")
	return cmd
}
