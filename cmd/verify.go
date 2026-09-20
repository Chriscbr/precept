package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/harness"
	"github.com/Chriscbr/precept/internal/prompt"
	"github.com/Chriscbr/precept/internal/report"
	"github.com/Chriscbr/precept/internal/verify"
	"github.com/spf13/cobra"
)

const (
	defaultJobs    = 4
	defaultTimeout = 10 * time.Minute
)

type verifyFlags struct {
	harness           string
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
	flags.StringVar(&options.harness, "harness", "", "agent harness: claude or codex (required)")
	flags.StringVar(&options.model, "model", "", "model override passed to the selected harness")
	flags.StringVar(&options.effort, "effort", "", "reasoning-effort override passed to the selected harness")
	flags.IntVar(&options.jobs, "jobs", defaultJobs, "maximum concurrent agent subprocesses")
	flags.DurationVar(&options.timeout, "timeout", defaultTimeout, "timeout for each claim")
	flags.StringArrayVar(&options.appendPrompt, "append-prompt", nil, "additional context appended to every verifier (repeatable)")
	flags.StringArrayVar(&options.appendPromptFiles, "append-prompt-file", nil, "file of context appended to every verifier (repeatable)")
	flags.BoolVar(&options.jsonOutput, "json", false, "emit JSON instead of text")
	return command
}

type verifyInvocation struct {
	command       *cobra.Command
	version       string
	scopeArgument string
	options       verifyFlags
	startedAt     time.Time
	log           *verificationLog
	display       *report.Progress
	runner        harness.Runner
	sections      []string
	scope         resolvedScope
	discovery     discover.Result
	info          harness.Info
	outcomes      []verify.Outcome
	report        report.Run
	err           error
}

func runVerify(command *cobra.Command, version, scopeArgument string, options verifyFlags, state *executionState) (returnErr error) {
	if state != nil {
		state.verificationLogPath = ""
	}
	if err := validateVerifyFlags(options); err != nil {
		return operationalError(err)
	}
	invocation := &verifyInvocation{
		command:       command,
		version:       version,
		scopeArgument: scopeArgument,
		options:       options,
		startedAt:     time.Now().UTC(),
	}
	if err := invocation.openLog(); err != nil {
		return operationalError(err)
	}
	if state != nil {
		state.verificationLogPath = invocation.log.Path()
	}
	invocation.display = report.NewProgress(command.OutOrStdout(), command.ErrOrStderr(), invocation.startedAt, options.jsonOutput)
	defer func() {
		if err := invocation.display.Close(); err != nil {
			returnErr = promoteLogFailure(returnErr, err)
		}
		if err := invocation.log.WriteCommandExit(returnErr); err != nil {
			returnErr = promoteLogFailure(returnErr, err)
		}
		if err := invocation.log.Close(); err != nil {
			returnErr = promoteLogFailure(returnErr, err)
		}
	}()
	invocation.execute()
	return invocation.err
}

func (invocation *verifyInvocation) openLog() error {
	var err error
	invocation.log, err = newVerificationLog(invocation.startedAt)
	return err
}

func (invocation *verifyInvocation) execute() {
	invocation.writeRunStart()
	invocation.selectRunner()
	invocation.preflight()
	invocation.writeHarness()
	invocation.loadContext()
	invocation.resolveScope()
	invocation.discoverClaims()
	invocation.writeDiscovery()
	invocation.writeDiscoveryStatus()
	invocation.validateClaims()
	invocation.buildReport()
	invocation.writeReport()
	invocation.setExitStatus()
}

func (invocation *verifyInvocation) writeRunStart() {
	if invocation.err == nil {
		invocation.fail(invocation.log.WriteRunStart(
			invocation.version,
			invocation.scopeArgument,
			invocation.options,
			invocation.startedAt,
		))
	}
}

func (invocation *verifyInvocation) selectRunner() {
	if invocation.err != nil {
		return
	}
	invocation.runner, invocation.err = harness.New(invocation.options.harness)
	invocation.wrapFailure()
}

func (invocation *verifyInvocation) loadContext() {
	if invocation.err != nil {
		return
	}
	invocation.sections, invocation.err = loadAppendedSections(
		invocation.options.appendPrompt,
		invocation.options.appendPromptFiles,
	)
	invocation.wrapFailure()
}

func (invocation *verifyInvocation) resolveScope() {
	if invocation.err != nil {
		return
	}
	invocation.fail(invocation.display.StartScan(invocation.scopeArgument))
	if invocation.err != nil {
		return
	}
	invocation.scope, invocation.err = resolveScope(invocation.command.Context(), invocation.scopeArgument)
	invocation.wrapFailure()
	if invocation.err == nil {
		invocation.display.SetRepositoryRoot(invocation.scope.repositoryRoot)
	}
}

func (invocation *verifyInvocation) discoverClaims() {
	if invocation.err != nil {
		return
	}
	invocation.discovery, invocation.err = discover.ScanContext(
		invocation.command.Context(),
		invocation.scope.repositoryRoot,
		invocation.scope.absolutePath,
	)
	if invocation.err != nil {
		invocation.err = operationalError(fmt.Errorf("discover claims: %w", invocation.err))
	}
}

func (invocation *verifyInvocation) writeDiscovery() {
	if invocation.err == nil {
		invocation.fail(invocation.display.EndScan(invocation.discovery))
		if invocation.err == nil {
			invocation.fail(invocation.log.WriteDiscovery(invocation.scope, invocation.discovery))
		}
	}
}

func (invocation *verifyInvocation) preflight() {
	if invocation.err != nil {
		return
	}
	invocation.info, invocation.err = invocation.runner.Preflight(invocation.command.Context())
	invocation.wrapFailure()
}

func (invocation *verifyInvocation) writeHarness() {
	if invocation.err == nil {
		invocation.fail(invocation.log.WriteHarness(invocation.info))
	}
}

func (invocation *verifyInvocation) writeDiscoveryStatus() {
	if invocation.err != nil {
		return
	}
	inline := 0
	for _, section := range invocation.options.appendPrompt {
		if strings.TrimSpace(section) != "" {
			inline++
		}
	}
	invocation.fail(invocation.display.StartVerification(invocation.scope.relativePath, len(invocation.discovery.Claims), report.Settings{
		Agent: invocation.runner.Name(), Model: invocation.options.model, Effort: invocation.options.effort,
		Jobs: invocation.options.jobs, Timeout: invocation.options.timeout,
		TextPrompts: inline, PromptFiles: len(invocation.sections) - inline,
	}))
}

func (invocation *verifyInvocation) validateClaims() {
	if invocation.err != nil {
		return
	}
	validator := &agentValidator{
		runner:           invocation.runner,
		repositoryRoot:   invocation.scope.repositoryRoot,
		scope:            invocation.scope.relativePath,
		model:            invocation.options.model,
		effort:           invocation.options.effort,
		appendedSections: invocation.sections,
		log:              invocation.log,
	}
	invocation.outcomes, invocation.err = verify.Run(
		invocation.command.Context(),
		invocation.discovery.Claims,
		validator,
		verify.Options{
			Jobs:    invocation.options.jobs,
			Timeout: invocation.options.timeout,
			Observe: invocation.display.Observe,
		},
	)
	invocation.wrapFailure()
}

func (invocation *verifyInvocation) buildReport() {
	if invocation.err != nil {
		return
	}
	invocation.report = report.Run{
		PreceptVersion: invocation.version,
		RepositoryRoot: invocation.scope.repositoryRoot,
		Scope:          invocation.scope.relativePath,
		Agent: report.Agent{
			Name:    invocation.info.Name,
			Version: invocation.info.Version,
			Model:   invocation.options.model,
			Effort:  invocation.options.effort,
		},
		StartedAt:       invocation.startedAt,
		FinishedAt:      time.Now().UTC(),
		AppendedContext: len(invocation.sections) > 0,
		Diagnostics:     invocation.discovery.Diagnostics,
		Outcomes:        invocation.outcomes,
	}
}

func (invocation *verifyInvocation) writeReport() {
	if invocation.err != nil {
		return
	}
	invocation.fail(invocation.log.WriteReport(invocation.report))
	if invocation.err != nil {
		return
	}
	invocation.fail(invocation.display.Finish(invocation.report))
}

func (invocation *verifyInvocation) setExitStatus() {
	if invocation.err != nil {
		return
	}
	switch report.ExitCode(invocation.outcomes) {
	case 1:
		invocation.err = semanticFailure()
	case 2:
		invocation.err = operationalFailure()
	}
}

func (invocation *verifyInvocation) fail(err error) {
	if err != nil && invocation.err == nil {
		invocation.err = operationalError(err)
	}
}

func (invocation *verifyInvocation) wrapFailure() {
	if invocation.err != nil {
		invocation.err = operationalError(invocation.err)
	}
}

func promoteLogFailure(commandErr, logErr error) error {
	if logErr == nil || errors.Is(commandErr, logErr) {
		return commandErr
	}
	if commandErr == nil || strings.TrimSpace(commandErr.Error()) == "" {
		return operationalError(logErr)
	}
	return operationalError(errors.Join(commandErr, logErr))
}

func validateVerifyFlags(options verifyFlags) error {
	if options.harness == "" {
		return fmt.Errorf("--harness is required (want claude or codex)")
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
