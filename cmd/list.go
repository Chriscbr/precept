package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/report"
	"github.com/spf13/cobra"
)

type listDocument struct {
	SchemaVersion int                   `json:"schema_version"`
	Scope         string                `json:"scope"`
	Claims        []discover.Claim      `json:"claims"`
	Diagnostics   []discover.Diagnostic `json:"diagnostics"`
}

func newListCommand() *cobra.Command {
	var jsonOutput bool
	var compact bool
	command := &cobra.Command{
		Use:   "list [file-or-directory]",
		Short: "List source-condition claims without launching an agent",
		Long:  "List INVARIANT, PRECONDITION, POSTCONDITION, and ASSERTION claims without launching an agent.\n\nIf file-or-directory is omitted, Precept scans the current working directory.",
		Args:  optionalScopeArgs,
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
			scopeArgument := scopeOrCurrent(args)
			display := report.NewProgress(cmd.OutOrStdout(), cmd.ErrOrStderr(), time.Now(), jsonOutput)
			defer func() {
				if err := display.Close(); err != nil {
					returnErr = promoteLogFailure(returnErr, err)
				}
			}()
			if err := display.StartScan(scopeArgument); err != nil {
				return operationalError(err)
			}
			scope, err := resolveScope(cmd.Context(), scopeArgument)
			if err != nil {
				return operationalError(err)
			}
			result, err := discover.ScanContext(cmd.Context(), scope.repositoryRoot, scope.absolutePath)
			if err != nil {
				return operationalError(fmt.Errorf("discover claims: %w", err))
			}

			if err := display.EndScan(result); err != nil {
				return operationalError(err)
			}

			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(listDocument{
					SchemaVersion: 4,
					Scope:         scope.relativePath,
					Claims:        result.Claims,
					Diagnostics:   result.Diagnostics,
				}); err != nil {
					return operationalError(fmt.Errorf("write JSON output: %w", err))
				}
				return nil
			}

			if err := report.WriteListText(cmd.OutOrStdout(), result.Claims, compact); err != nil {
				return operationalError(err)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON instead of text")
	command.Flags().BoolVar(&compact, "compact", false, "show a compact index without claim text (ignored with --json)")
	return command
}
