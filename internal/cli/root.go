// Package cli provides Strata's command-line interface.
package cli

import (
	"context"
	"fmt"

	"github.com/mexirica/strata/internal/repository"
	"github.com/spf13/cobra"
)

type application struct {
	configPath string
}

func NewRootCommand() *cobra.Command {
	app := &application{}
	root := &cobra.Command{
		Use:           "strata",
		Short:         "Content-addressed file storage",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&app.configPath, "config", "strata.yaml", "configuration file")
	root.AddCommand(
		app.initCommand(),
		app.addCommand(),
		app.getCommand(),
		app.listCommand(),
		app.removeCommand(),
		app.gcCommand(),
		app.scrubCommand(),
	)
	return root
}

func Execute() error {
	return NewRootCommand().Execute()
}

// withNode owns the repository lifetime for one command. An operation failure
// takes precedence over a close failure so the original cause is retained.
func (a *application) withNode(ctx context.Context, operation func(*repository.Node) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return err
	}
	nodeConfig, err := cfg.nodeConfig()
	if err != nil {
		return err
	}
	n, err := repository.Open(ctx, nodeConfig)
	if err != nil {
		return fmt.Errorf("open repository: %w", err)
	}
	operationErr := operation(n)
	if closeErr := n.Close(); operationErr == nil && closeErr != nil {
		return fmt.Errorf("close repository: %w", closeErr)
	}
	return operationErr
}
