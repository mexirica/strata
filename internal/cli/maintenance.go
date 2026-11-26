package cli

import (
	"fmt"

	"github.com/mexirica/strata/internal/maintenance"
	"github.com/mexirica/strata/internal/repository"
	"github.com/spf13/cobra"
)

func (a *application) gcCommand() *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   "gc",
		Short: "Remove unreferenced chunks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withNode(cmd.Context(), func(n *repository.Node) error {
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
			return a.withNode(cmd.Context(), func(n *repository.Node) error {
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
