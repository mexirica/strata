package cli

import (
	"fmt"

	"github.com/mexirica/strata/internal/content/chunker"
	"github.com/mexirica/strata/internal/repository"
	"github.com/spf13/cobra"
)

func (a *application) initCommand() *cobra.Command {
	defaultChunking := chunker.DefaultConfig()
	command := &cobra.Command{
		Use:   "init",
		Short: "Initialize a Strata repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ensureDefaultConfig(a.configPath); err != nil {
				return err
			}
			cfg, err := loadConfig(a.configPath)
			if err != nil {
				return err
			}
			nodeConfig, err := cfg.nodeConfig()
			if err != nil {
				return err
			}
			initChunking, err := loadInitChunking(cmd.Flags())
			if err != nil {
				return err
			}
			n, err := repository.Init(cmd.Context(), repository.InitConfig{
				Config:   nodeConfig,
				Chunking: initChunking,
			})
			if err != nil {
				return fmt.Errorf("initialize repository: %w", err)
			}
			if err := n.Close(); err != nil {
				return fmt.Errorf("close repository: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized repository in %s\n", cfg.DataDir)
			return nil
		},
	}
	command.Flags().Int("min-chunk-size", defaultChunking.MinSize, "minimum chunk size for this repository")
	command.Flags().Int("normal-chunk-size", defaultChunking.NormalSize, "target chunk size for this repository")
	command.Flags().Int("max-chunk-size", defaultChunking.MaxSize, "maximum chunk size for this repository")
	return command
}
