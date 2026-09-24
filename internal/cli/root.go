// Package cli provides Strata's command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/maintenance"
	"github.com/mexirica/strata/internal/node"
	"github.com/spf13/cobra"
)

const listPageSize = 1000

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

func (a *application) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a Strata repository configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := writeDefaultConfig(a.configPath); err != nil {
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
			n, err := node.New(cmd.Context(), nodeConfig)
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
}

func (a *application) addCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "add <file>",
		Short: "Add a file to the repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open input file: %w", err)
			}
			defer file.Close()
			return a.withNode(cmd.Context(), func(n *node.Node) error {
				manifestCID, err := n.Store(cmd.Context(), filepath.Base(args[0]), file)
				if err != nil {
					return fmt.Errorf("add file: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), manifestCID.String())
				return nil
			})
		},
	}
}

func (a *application) getCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <cid> <destination>",
		Short: "Retrieve a file from the repository",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestCID, err := cid.ParseCID(args[0])
			if err != nil {
				return fmt.Errorf("parse CID: %w", err)
			}
			return a.withNode(cmd.Context(), func(n *node.Node) error {
				reader, _, err := n.Retrieve(cmd.Context(), manifestCID)
				if err != nil {
					return fmt.Errorf("retrieve file: %w", err)
				}
				defer reader.Close()
				return writeDestination(args[1], reader)
			})
		},
	}
}

func writeDestination(destination string, reader io.Reader) error {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".strata-get-*")
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := io.Copy(temporary, reader); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write destination: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	return nil
}

func (a *application) listCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withNode(cmd.Context(), func(n *node.Node) error {
				writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(writer, "CID\tSIZE\tNAME")
				var cursor *cid.CID
				for {
					manifests, next, err := n.List(cmd.Context(), cursor, listPageSize)
					if err != nil {
						return fmt.Errorf("list files: %w", err)
					}
					for _, stored := range manifests {
						fmt.Fprintf(writer, "%s\t%s\t%s\n", stored.CID.String(), humanize.Bytes(uint64(stored.Manifest.Size)), stored.Manifest.Name)
					}
					if next == nil {
						break
					}
					cursor = next
				}
				return writer.Flush()
			})
		},
	}
}

func (a *application) removeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <cid>",
		Short: "Remove a file manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestCID, err := cid.ParseCID(args[0])
			if err != nil {
				return fmt.Errorf("parse CID: %w", err)
			}
			return a.withNode(cmd.Context(), func(n *node.Node) error {
				if err := n.Delete(cmd.Context(), manifestCID); err != nil {
					return fmt.Errorf("remove file: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), manifestCID.String())
				return nil
			})
		},
	}
}

func (a *application) gcCommand() *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   "gc",
		Short: "Remove unreferenced chunks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withNode(cmd.Context(), func(n *node.Node) error {
				report, err := n.RunGC(cmd.Context(), dryRun)
				if err != nil {
					return fmt.Errorf("run garbage collection: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "manifests=%d referenced=%d scanned=%d deleted=%d dry_run=%t\n",
					report.ManifestsScanned, report.ChunksReferenced, report.ChunksScanned, report.ChunksDeleted, dryRun)
				return nil
			})
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report without deleting chunks")
	return command
}

func (a *application) scrubCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "scrub",
		Short: "Check repository integrity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withNode(cmd.Context(), func(n *node.Node) error {
				issues, err := n.Scrub(cmd.Context())
				if err != nil {
					return fmt.Errorf("scrub repository: %w", err)
				}
				for _, issue := range issues {
					fmt.Fprintln(cmd.OutOrStdout(), formatScrubIssue(issue))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "issues=%d\n", len(issues))
				if len(issues) > 0 {
					return fmt.Errorf("repository integrity check found %d issue(s)", len(issues))
				}
				return nil
			})
		},
	}
}

func formatScrubIssue(issue maintenance.ScrubIssue) string {
	result := fmt.Sprintf("kind=%d", issue.Kind)
	if issue.ManifestCID != nil {
		result += " manifest=" + issue.ManifestCID.String()
	}
	if issue.ChunkCID != nil {
		result += " chunk=" + issue.ChunkCID.String()
	}
	if issue.Err != nil {
		result += " error=" + issue.Err.Error()
	}
	return result
}

func (a *application) withNode(ctx context.Context, operation func(*node.Node) error) error {
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
	n, err := node.New(ctx, nodeConfig)
	if err != nil {
		return fmt.Errorf("open repository: %w", err)
	}
	operationErr := operation(n)
	if closeErr := n.Close(); operationErr == nil && closeErr != nil {
		return fmt.Errorf("close repository: %w", closeErr)
	}
	return operationErr
}
