package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/Chriscbr/precept/internal/textsafe"
)

type exitError struct {
	code int
	err  error
}

type executionState struct {
	verificationLogPath string
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error {
	return e.err
}

func operationalError(err error) error {
	return &exitError{code: 2, err: err}
}

func semanticFailure() error {
	return &exitError{code: 1}
}

func operationalFailure() error {
	return &exitError{code: 2}
}

func optionalScopeArgs(_ *cobra.Command, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf(
			"expected at most one file-or-directory argument, received %d; omit it to scan the current directory",
			len(args),
		)
	}
	return nil
}

func scopeOrCurrent(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}

// Execute runs the Precept CLI and returns its process exit code.
func Execute(version string) int {
	root, state := newRootCommand(version)
	root.SetIn(os.Stdin)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return executeRoot(ctx, root, state)
}

func executeRoot(ctx context.Context, root *cobra.Command, state *executionState) int {
	err := root.ExecuteContext(ctx)
	exitCode := 0

	var exitErr *exitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exitCode = exitErr.code
		if exitErr.err != nil {
			if _, writeErr := fmt.Fprintf(root.ErrOrStderr(), "error: %s\n", textsafe.SingleLine(exitErr.err.Error())); writeErr != nil {
				exitCode = 2
			}
		}
	default:
		exitCode = 2
		_, _ = fmt.Fprintf(root.ErrOrStderr(), "error: %s\n", textsafe.SingleLine(err.Error()))
	}

	if state != nil && state.verificationLogPath != "" {
		if _, writeErr := fmt.Fprintf(root.ErrOrStderr(), "Log: %s\n", state.verificationLogPath); writeErr != nil {
			exitCode = 2
		}
	}
	return exitCode
}

// NewRootCommand creates the Cobra command tree. It is exported for tests.
func NewRootCommand(version string) *cobra.Command {
	root, _ := newRootCommand(version)
	return root
}

func newRootCommand(version string) (*cobra.Command, *executionState) {
	state := &executionState{}
	root := &cobra.Command{
		Use:           "precept",
		Short:         "Validate claims about source code",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		newListCommand(),
		newVerifyCommand(version, state),
		&cobra.Command{
			Use:   "version",
			Short: "Print the Precept version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := writeFormatted(cmd.OutOrStdout(), "%s\n", version); err != nil {
					return operationalError(err)
				}
				return nil
			},
		},
	)
	return root, state
}

func writeFormatted(writer io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(writer, format, arguments...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
