package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/repository"
	"github.com/spf13/cobra"
)

const listPageSize = 1000

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
			return a.withNode(cmd.Context(), func(n *repository.Node) error {
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
			return a.withNode(cmd.Context(), func(n *repository.Node) error {
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

// writeDestination stages the stream beside the destination and renames only
// after a successful copy and close, preserving an existing file on read errors.
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
			return a.withNode(cmd.Context(), func(n *repository.Node) error {
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
			return a.withNode(cmd.Context(), func(n *repository.Node) error {
				if err := n.Delete(cmd.Context(), manifestCID); err != nil {
					return fmt.Errorf("remove file: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), manifestCID.String())
				return nil
			})
		},
	}
}
