package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"precept/internal/discover"
	"precept/internal/harness"
	"precept/internal/prompt"
	"precept/internal/report"
	"precept/internal/textsafe"
	"precept/internal/verify"
)

const (
	defaultJobs    = 4
	defaultTimeout = 10 * time.Minute
)

type verifyFlags struct {
	agent             string
	model             string
	effort            string
	jobs              int
	timeout           time.Duration
	appendPrompt      []string
	appendPromptFiles []string
	jsonOutput        bool
}

func newVerifyCommand(version string, state *executionState) *cobra.Command {
	options := verifyFlags{}
	command := &cobra.Command{
		Use:   "verify [file-or-directory]",
		Short: "Check source-condition claims using local coding agents.",
		Long:  "Check INVARIANT, PRECONDITION, POSTCONDITION, and ASSERTION claims using local coding agents.\n\nIf file-or-directory is omitted, precept scans the current working directory.",
		Args:  optionalScopeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerify(cmd, version, scopeOrCurrent(args), options, state)
		},
	}

	flags := command.Flags()
	flags.StringVar(&options.agent, "agent", "", "coding-agent harness: claude or codex (required)")
	flags.StringVar(&options.model, "model", "", "model override passed to the selected agent")
	flags.StringVar(&options.effort, "effort", "", "reasoning-effort override passed to the selected agent")
	flags.IntVar(&options.jobs, "jobs", defaultJobs, "maximum concurrent agent subprocesses")
	flags.DurationVar(&options.timeout, "timeout", defaultTimeout, "timeout for each claim")
	flags.StringArrayVar(&options.appendPrompt, "append-prompt", nil, "additional context appended to every verifier (repeatable)")
	flags.StringArrayVar(&options.appendPromptFiles, "append-prompt-file", nil, "file of context appended to every verifier (repeatable)")
	flags.BoolVar(&options.jsonOutput, "json", false, "emit JSON instead of text")
	return command
}

func runVerify(command *cobra.Command, version, scopeArgument string, options verifyFlags, state *executionState) (returnErr error) {
	if state != nil {
		state.verificationLogPath = ""
	}
	if err := validateVerifyFlags(options); err != nil {
		return operationalError(err)
	}
	startedAt := time.Now().UTC()
	if err := writeFormatted(command.ErrOrStderr(), "Scanning for claims in %s...\n", textsafe.SingleLine(scopeArgument)); err != nil {
		return operationalError(err)
	}
	runLog, err := newVerificationLog(startedAt)
	if err != nil {
		return operationalError(err)
	}
	if state != nil {
		state.verificationLogPath = runLog.Path()
	}
	defer func() {
		if err := runLog.WriteCommandExit(returnErr); err != nil {
			returnErr = promoteLogFailure(returnErr, err)
		}
		if err := runLog.Close(); err != nil {
			returnErr = promoteLogFailure(returnErr, err)
		}
	}()
	if err := runLog.WriteRunStart(version, scopeArgument, options, startedAt); err != nil {
		return operationalError(err)
	}

	runner, err := harness.New(options.agent)
	if err != nil {
		return operationalError(err)
	}
	sections, err := loadAppendedSections(options.appendPrompt, options.appendPromptFiles)
	if err != nil {
		return operationalError(err)
	}
	scope, err := resolveScope(command.Context(), scopeArgument)
	if err != nil {
		return operationalError(err)
	}
	discovery, err := discover.ScanContext(command.Context(), scope.repositoryRoot, scope.absolutePath)
	if err != nil {
		return operationalError(fmt.Errorf("discover claims: %w", err))
	}
	if err := runLog.WriteDiscovery(scope, discovery); err != nil {
		return operationalError(err)
	}

	info := harness.Info{Name: runner.Name()}
	if len(discovery.Claims) > 0 {
		info, err = runner.Preflight(command.Context())
		if err != nil {
			return operationalError(err)
		}
	}
	if err := runLog.WriteHarness(info); err != nil {
		return operationalError(err)
	}
	if len(discovery.Claims) == 0 {
		err = writeFormatted(command.ErrOrStderr(), "Discovered 0 claim(s) in %s; nothing to validate\n", textsafe.SingleLine(scope.relativePath))
	} else {
		err = writeFormatted(
			command.ErrOrStderr(),
			"Discovered %d claim(s) in %s. Validating with %s using up to %d parallel worker(s)\n",
			len(discovery.Claims),
			textsafe.SingleLine(scope.relativePath),
			textsafe.SingleLine(runner.Name()),
			options.jobs,
		)
	}
	if err != nil {
		return operationalError(err)
	}

	validator := &agentValidator{
		runner:           runner,
		repositoryRoot:   scope.repositoryRoot,
		scope:            scope.relativePath,
		model:            options.model,
		effort:           options.effort,
		appendedSections: sections,
		log:              runLog,
	}
	outcomes, err := verify.Run(command.Context(), discovery.Claims, validator, verify.Options{
		Jobs:     options.jobs,
		Timeout:  options.timeout,
		Progress: command.ErrOrStderr(),
	})
	if err != nil {
		return operationalError(err)
	}

	run := report.Run{
		PreceptVersion: version,
		Scope:          scope.relativePath,
		Agent: report.Agent{
			Name:    info.Name,
			Version: info.Version,
			Model:   options.model,
			Effort:  options.effort,
		},
		StartedAt:       startedAt,
		FinishedAt:      time.Now().UTC(),
		AppendedContext: len(sections) > 0,
		Diagnostics:     discovery.Diagnostics,
		Outcomes:        outcomes,
	}
	outputFormat := report.FormatText
	if options.jsonOutput {
		outputFormat = report.FormatJSON
	}
	if err := runLog.WriteReport(run); err != nil {
		return operationalError(err)
	}
	if err := report.Write(command.OutOrStdout(), outputFormat, run); err != nil {
		return operationalError(err)
	}

	switch report.ExitCode(outcomes) {
	case 0:
		return nil
	case 1:
		return semanticFailure()
	default:
		return operationalFailure()
	}
}

func promoteLogFailure(commandErr, logErr error) error {
	if logErr == nil {
		return commandErr
	}
	if commandErr == nil || strings.TrimSpace(commandErr.Error()) == "" {
		return operationalError(logErr)
	}
	return operationalError(errors.Join(commandErr, logErr))
}

func validateVerifyFlags(options verifyFlags) error {
	if options.agent == "" {
		return fmt.Errorf("--agent is required (want claude or codex)")
	}
	if options.jobs <= 0 {
		return fmt.Errorf("--jobs must be positive")
	}
	if options.timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	return nil
}

func loadAppendedSections(inline, filenames []string) ([]string, error) {
	sections := make([]string, 0, len(inline)+len(filenames))
	for _, section := range inline {
		if strings.TrimSpace(section) != "" {
			sections = append(sections, section)
		}
	}
	for _, filename := range filenames {
		contents, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read appended prompt file %q: %w", filename, err)
		}
		if strings.TrimSpace(string(contents)) != "" {
			sections = append(sections, string(contents))
		}
	}
	return sections, nil
}

type agentValidator struct {
	runner           harness.Runner
	repositoryRoot   string
	scope            string
	model            string
	effort           string
	appendedSections []string
	log              *verificationLog
}

func (validator *agentValidator) Validate(ctx context.Context, claim discover.Claim) (verify.Validation, error) {
	rendered, err := prompt.Render(prompt.Data{
		RepositoryRoot:   validator.repositoryRoot,
		Scope:            validator.scope,
		Marker:           string(claim.Marker),
		Claim:            claim.Text,
		Package:          claim.Package,
		SubjectKind:      claim.Kind,
		Symbol:           claim.Symbol,
		File:             claim.File,
		MarkerLine:       claim.MarkerLine,
		SubjectStartLine: claim.StartLine,
		SubjectEndLine:   claim.EndLine,
		AppendedSections: validator.appendedSections,
	})
	if err != nil {
		if validator.log == nil {
			return verify.Validation{}, err
		}
		logErr := validator.log.WriteAgentSession(validator.runner.Name(), claim, harness.RunResult{}, true)
		return verify.Validation{}, errors.Join(err, logErr)
	}
	run, runErr := validator.runner.Run(ctx, harness.Request{
		RepositoryRoot: validator.repositoryRoot,
		Prompt:         rendered,
		Model:          validator.model,
		Effort:         validator.effort,
	})
	var logErr error
	if validator.log != nil {
		logErr = validator.log.WriteAgentSession(validator.runner.Name(), claim, run, runErr != nil)
	}

	evidence := make([]verify.Evidence, 0, len(run.Result.Evidence))
	for _, item := range run.Result.Evidence {
		evidence = append(evidence, verify.Evidence{
			File:      item.File,
			StartLine: item.StartLine,
			EndLine:   item.EndLine,
			Reason:    item.Reason,
		})
	}
	return verify.Validation{
		Result: verify.Result{
			Verdict:        verify.Verdict(run.Result.Verdict),
			Summary:        run.Result.Summary,
			Evidence:       evidence,
			Counterexample: run.Result.Counterexample,
		},
		SessionID: run.SessionID,
	}, errors.Join(runErr, logErr)
}
