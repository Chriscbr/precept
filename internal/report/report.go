// Package report renders deterministic discovery and verification results for humans and tools.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/textsafe"
	"github.com/Chriscbr/precept/internal/verify"
	"golang.org/x/term"
)

// JSONSchemaVersion changes when the JSON document contract changes incompatibly.
const JSONSchemaVersion = 4

// Format selects a report encoding.
type Format string

const (
	// FormatText is the concise human-readable report.
	FormatText Format = "text"
	// FormatJSON is the versioned machine-readable report.
	FormatJSON Format = "json"
)

// Agent identifies the harness used for a run. Empty model and effort values mean
// the harness defaults were left unchanged.
type Agent struct {
	Name    string
	Version string
	Model   string
	Effort  string
}

// Run contains all data needed to render one verification invocation.
type Run struct {
	PreceptVersion  string
	Scope           string
	Agent           Agent
	StartedAt       time.Time
	FinishedAt      time.Time
	AppendedContext bool
	Diagnostics     []discover.Diagnostic
	Outcomes        []verify.Outcome
}

// Summary counts semantic and operational outcome classes.
type Summary struct {
	Total        int
	Holds        int
	Violated     int
	Inconclusive int
	Errors       int
}

// Summarize counts outcomes. Structurally invalid results count as errors.
func Summarize(outcomes []verify.Outcome) Summary {
	summary := Summary{Total: len(outcomes)}
	for _, outcome := range outcomes {
		if operationalError(outcome) != "" {
			summary.Errors++
			continue
		}
		switch outcome.Result.Verdict {
		case verify.VerdictHolds:
			summary.Holds++
		case verify.VerdictViolated:
			summary.Violated++
		case verify.VerdictInconclusive:
			summary.Inconclusive++
		case verify.VerdictError:
			summary.Errors++
		}
	}
	return summary
}

// ExitCode implements Precept's process exit contract. Error outcomes take
// precedence over violated or inconclusive claims.
func ExitCode(outcomes []verify.Outcome) int {
	summary := Summarize(outcomes)
	if summary.Errors > 0 {
		return 2
	}
	if summary.Violated > 0 || summary.Inconclusive > 0 {
		return 1
	}
	return 0
}

// Write renders run in the selected format.
func Write(writer io.Writer, format Format, run Run) error {
	switch format {
	case FormatText:
		return WriteText(writer, run)
	case FormatJSON:
		return WriteJSON(writer, run)
	default:
		return fmt.Errorf("unsupported report format %q", format)
	}
}

// WriteText renders the concise human-readable report. ANSI styling is enabled
// only for terminal writers when the user's environment permits color.
func WriteText(writer io.Writer, run Run) error {
	return writeText(writer, run, textStyle{enabled: shouldStyle(writer, os.LookupEnv)})
}

func writeText(writer io.Writer, run Run, style textStyle) error {
	renderer := &textReportRenderer{writer: writer, run: run, style: style}
	renderer.writeDiagnostics()
	renderer.writeDiagnosticSeparator()
	renderer.writeOutcomes()
	renderer.writeSummarySeparator()
	renderer.writeSummary()
	return renderer.err
}

func writeTextOutcome(writer io.Writer, agentName string, outcome verify.Outcome, style textStyle) error {
	renderer := &textOutcomeRenderer{
		writer:       writer,
		agentName:    agentName,
		outcome:      outcome,
		style:        style,
		errorMessage: operationalError(outcome),
		mark:         "!",
		status:       "Error",
		markColor:    ansiRed,
	}
	renderer.selectStatus()
	renderer.writeHeader()
	renderer.writeStatus()
	renderer.writeReason()
	renderer.writeEvidence()
	renderer.writeCounterexample()
	renderer.writeSession()
	return renderer.err
}

type textReportRenderer struct {
	writer io.Writer
	run    Run
	style  textStyle
	err    error
}

func (renderer *textReportRenderer) writeDiagnostics() {
	for _, diagnostic := range renderer.run.Diagnostics {
		if renderer.err != nil {
			break
		}
		location := textsafe.SingleLine(diagnostic.File)
		if diagnostic.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, diagnostic.Line)
		}
		_, renderer.err = fmt.Fprintf(
			renderer.writer,
			"warning %s: %s\n",
			location,
			textsafe.SingleLine(diagnostic.Message),
		)
		if renderer.err != nil {
			renderer.err = fmt.Errorf("write diagnostic: %w", renderer.err)
		}
	}
}

func (renderer *textReportRenderer) writeDiagnosticSeparator() {
	if renderer.err != nil || len(renderer.run.Diagnostics) == 0 || len(renderer.run.Outcomes) == 0 {
		return
	}
	_, renderer.err = io.WriteString(renderer.writer, "\n")
	if renderer.err != nil {
		renderer.err = fmt.Errorf("write report separator: %w", renderer.err)
	}
}

func (renderer *textReportRenderer) writeOutcomes() {
	for index, outcome := range renderer.run.Outcomes {
		if renderer.err != nil {
			break
		}
		renderer.err = writeTextOutcome(renderer.writer, renderer.run.Agent.Name, outcome, renderer.style)
		if renderer.err == nil && index+1 < len(renderer.run.Outcomes) {
			_, renderer.err = io.WriteString(renderer.writer, "\n")
			if renderer.err != nil {
				renderer.err = fmt.Errorf("write outcome separator: %w", renderer.err)
			}
		}
	}
}

func (renderer *textReportRenderer) writeSummarySeparator() {
	if renderer.err != nil || len(renderer.run.Outcomes) == 0 {
		return
	}
	_, renderer.err = io.WriteString(renderer.writer, "\n")
	if renderer.err != nil {
		renderer.err = fmt.Errorf("write report separator: %w", renderer.err)
	}
}

func (renderer *textReportRenderer) writeSummary() {
	if renderer.err != nil {
		return
	}
	summary := Summarize(renderer.run.Outcomes)
	errorNoun := "errors"
	if summary.Errors == 1 {
		errorNoun = "error"
	}
	_, renderer.err = fmt.Fprintf(
		renderer.writer,
		"%d claims: %d holds, %d violated, %d inconclusive, %d %s\n",
		summary.Total,
		summary.Holds,
		summary.Violated,
		summary.Inconclusive,
		summary.Errors,
		errorNoun,
	)
	if renderer.err != nil {
		renderer.err = fmt.Errorf("write report summary: %w", renderer.err)
	}
}

type textOutcomeRenderer struct {
	writer       io.Writer
	agentName    string
	outcome      verify.Outcome
	style        textStyle
	errorMessage string
	mark         string
	status       string
	markColor    string
	err          error
}

func (renderer *textOutcomeRenderer) selectStatus() {
	if renderer.errorMessage != "" {
		return
	}
	switch renderer.outcome.Result.Verdict {
	case verify.VerdictHolds:
		renderer.mark, renderer.status, renderer.markColor = "✓", "Holds", ansiGreen
	case verify.VerdictViolated:
		renderer.mark, renderer.status, renderer.markColor = "✗", "Violated", ansiRed
	case verify.VerdictInconclusive:
		renderer.mark, renderer.status, renderer.markColor = "?", "Inconclusive", ansiYellow
	case verify.VerdictError:
		renderer.mark, renderer.status, renderer.markColor = "!", "Error", ansiRed
	}
}

func (renderer *textOutcomeRenderer) writeHeader() {
	_, renderer.err = fmt.Fprintf(
		renderer.writer,
		"%s %s [%s] (%s:%d)\n",
		renderer.style.color(renderer.markColor, renderer.mark),
		renderer.style.bold(textsafe.SingleLine(displaySymbol(renderer.outcome.Claim))),
		textsafe.SingleLine(string(renderer.outcome.Claim.Marker)),
		textsafe.SingleLine(renderer.outcome.Claim.File),
		renderer.outcome.Claim.MarkerLine,
	)
	if renderer.err != nil {
		renderer.err = fmt.Errorf("write outcome: %w", renderer.err)
	}
}

func (renderer *textOutcomeRenderer) writeStatus() {
	if renderer.err == nil {
		renderer.err = writeLabeledValue(
			renderer.writer,
			renderer.style,
			"outcome",
			renderer.style.color(renderer.markColor, renderer.status),
		)
	}
}

func (renderer *textOutcomeRenderer) writeReason() {
	if renderer.err != nil {
		return
	}
	reason := renderer.errorMessage
	if reason == "" {
		// The agent-provided summary is concise, disclosed reasoning. Raw agent
		// traces and hidden chain-of-thought are deliberately not report inputs.
		reason = renderer.outcome.Result.Summary
	}
	renderer.err = writeLabeledValue(renderer.writer, renderer.style, "reason", textsafe.Sanitize(reason))
}

func (renderer *textOutcomeRenderer) writeEvidence() {
	if renderer.err != nil || renderer.errorMessage != "" {
		return
	}
	_, renderer.err = fmt.Fprintln(renderer.writer, "  "+renderer.style.bold(formatLabel("supporting evidence")))
	if renderer.err != nil {
		renderer.err = fmt.Errorf("write supporting evidence label: %w", renderer.err)
		return
	}
	for _, evidence := range renderer.outcome.Result.Evidence {
		renderer.err = writeEvidenceBullet(renderer.writer, renderer.style, evidence)
		if renderer.err != nil {
			break
		}
	}
}

func (renderer *textOutcomeRenderer) writeCounterexample() {
	if renderer.err != nil || renderer.errorMessage != "" {
		return
	}
	counterexample := strings.TrimSpace(renderer.outcome.Result.Counterexample)
	if counterexample != "" {
		renderer.err = writeLabeledValue(
			renderer.writer,
			renderer.style,
			"counterexample",
			textsafe.Sanitize(renderer.outcome.Result.Counterexample),
		)
	}
}

func (renderer *textOutcomeRenderer) writeSession() {
	if renderer.err == nil {
		renderer.err = writeAgentSession(
			renderer.writer,
			renderer.style,
			renderer.agentName,
			renderer.outcome.SessionID,
		)
	}
}

func writeAgentSession(writer io.Writer, style textStyle, agentName, sessionID string) error {
	command := ResumeCommand(agentName, sessionID)
	if command == "" {
		return nil
	}
	return writeLabeledValue(writer, style, "agent session", textsafe.SingleLine(command))
}

func writeLabeledValue(writer io.Writer, style textStyle, label, value string) error {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		prefix := "  " + style.bold(formatLabel(label)) + " "
		if index > 0 {
			prefix = strings.Repeat(" ", len(label)+6)
		}
		if _, err := fmt.Fprintln(writer, prefix+line); err != nil {
			return fmt.Errorf("write %s: %w", label, err)
		}
	}
	return nil
}

func formatLabel(label string) string {
	return "[" + label + "]:"
}

func writeEvidenceBullet(writer io.Writer, style textStyle, evidence verify.Evidence) error {
	location := fmt.Sprintf("%s:%d", textsafe.SingleLine(evidence.File), evidence.StartLine)
	if evidence.EndLine != evidence.StartLine {
		location = fmt.Sprintf("%s-%d", location, evidence.EndLine)
	}
	lines := strings.Split(textsafe.Sanitize(evidence.Reason), "\n")
	for index, line := range lines {
		prefix := "    • "
		if index > 0 {
			prefix = "      "
		}
		if index+1 == len(lines) {
			line += " " + style.gray("("+location+")")
		}
		if _, err := fmt.Fprintln(writer, prefix+line); err != nil {
			return fmt.Errorf("write supporting evidence: %w", err)
		}
	}
	return nil
}

const (
	ansiBold   = "1"
	ansiRed    = "31"
	ansiGreen  = "32"
	ansiYellow = "33"
	ansiCyan   = "36"
	ansiGray   = "90"
)

type textStyle struct {
	enabled bool
}

func (style textStyle) bold(value string) string {
	return style.color(ansiBold, value)
}

func (style textStyle) gray(value string) string {
	return style.color(ansiGray, value)
}

func (style textStyle) color(code, value string) string {
	if !style.enabled {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

type fileDescriptorWriter interface {
	Fd() uintptr
}

func shouldStyle(writer io.Writer, lookupEnv func(string) (string, bool)) bool {
	if !colorEnvironmentAllows(lookupEnv) {
		return false
	}
	output, ok := writer.(fileDescriptorWriter)
	return ok && term.IsTerminal(int(output.Fd()))
}

func colorEnvironmentAllows(lookupEnv func(string) (string, bool)) bool {
	if _, present := lookupEnv("NO_COLOR"); present {
		return false
	}
	terminal, _ := lookupEnv("TERM")
	return !strings.EqualFold(strings.TrimSpace(terminal), "dumb")
}

func displaySymbol(claim discover.Claim) string {
	if claim.Kind == "package" {
		return "package " + claim.Package
	}
	if claim.Package == "" || strings.HasPrefix(claim.Symbol, "(") {
		return claim.Symbol
	}
	return claim.Package + "." + claim.Symbol
}

// WriteJSON renders the versioned machine-readable document. Errors are encoded
// as strings instead of Go error interface objects.
func WriteJSON(writer io.Writer, run Run) error {
	document := makeJSONDocument(run)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	return nil
}

type jsonDocument struct {
	SchemaVersion   int              `json:"schema_version"`
	PreceptVersion  string           `json:"precept_version"`
	Scope           string           `json:"scope"`
	Agent           jsonAgent        `json:"agent"`
	Timing          jsonTiming       `json:"timing"`
	AppendedContext bool             `json:"appended_context"`
	Diagnostics     []jsonDiagnostic `json:"diagnostics"`
	Outcomes        []jsonOutcome    `json:"outcomes"`
	Summary         jsonSummary      `json:"summary"`
}

type jsonAgent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

type jsonTiming struct {
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	DurationMS int64  `json:"duration_ms"`
}

type jsonDiagnostic struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type jsonClaim struct {
	Marker     discover.Marker `json:"marker"`
	Text       string          `json:"text"`
	Package    string          `json:"package"`
	Kind       string          `json:"kind"`
	Symbol     string          `json:"symbol"`
	File       string          `json:"file"`
	MarkerLine int             `json:"marker_line"`
	StartLine  int             `json:"start_line"`
	EndLine    int             `json:"end_line"`
}

type jsonOutcome struct {
	Claim         jsonClaim   `json:"claim"`
	Result        *jsonResult `json:"result,omitempty"`
	Error         string      `json:"error,omitempty"`
	SessionID     string      `json:"session_id,omitempty"`
	ResumeCommand string      `json:"resume_command,omitempty"`
	DurationMS    int64       `json:"duration_ms"`
}

type jsonResult struct {
	Verdict        verify.Verdict `json:"verdict"`
	Summary        string         `json:"summary"`
	Evidence       []jsonEvidence `json:"evidence"`
	Counterexample string         `json:"counterexample"`
}

type jsonEvidence struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Reason    string `json:"reason"`
}

type jsonSummary struct {
	Total        int `json:"total"`
	Holds        int `json:"holds"`
	Violated     int `json:"violated"`
	Inconclusive int `json:"inconclusive"`
	Errors       int `json:"errors"`
}

func makeJSONDocument(run Run) jsonDocument {
	diagnostics := make([]jsonDiagnostic, 0, len(run.Diagnostics))
	for _, diagnostic := range run.Diagnostics {
		diagnostics = append(diagnostics, jsonDiagnostic{
			File:    diagnostic.File,
			Line:    diagnostic.Line,
			Message: diagnostic.Message,
		})
	}
	outcomes := make([]jsonOutcome, 0, len(run.Outcomes))
	for _, outcome := range run.Outcomes {
		sessionID := strings.TrimSpace(outcome.SessionID)
		jsonValue := jsonOutcome{
			Claim: jsonClaim{
				Marker:     outcome.Claim.Marker,
				Text:       outcome.Claim.Text,
				Package:    outcome.Claim.Package,
				Kind:       outcome.Claim.Kind,
				Symbol:     outcome.Claim.Symbol,
				File:       outcome.Claim.File,
				MarkerLine: outcome.Claim.MarkerLine,
				StartLine:  outcome.Claim.StartLine,
				EndLine:    outcome.Claim.EndLine,
			},
			SessionID:     sessionID,
			ResumeCommand: ResumeCommand(run.Agent.Name, sessionID),
			DurationMS:    durationMilliseconds(outcome.Duration),
		}
		if errorMessage := operationalError(outcome); errorMessage != "" {
			jsonValue.Error = errorMessage
		} else {
			jsonValue.Result = makeJSONResult(*outcome.Result)
		}
		outcomes = append(outcomes, jsonValue)
	}

	summary := Summarize(run.Outcomes)
	return jsonDocument{
		SchemaVersion:  JSONSchemaVersion,
		PreceptVersion: run.PreceptVersion,
		Scope:          run.Scope,
		Agent: jsonAgent{
			Name:    run.Agent.Name,
			Version: run.Agent.Version,
			Model:   run.Agent.Model,
			Effort:  run.Agent.Effort,
		},
		Timing: jsonTiming{
			StartedAt:  formatTime(run.StartedAt),
			FinishedAt: formatTime(run.FinishedAt),
			DurationMS: durationMilliseconds(run.FinishedAt.Sub(run.StartedAt)),
		},
		AppendedContext: run.AppendedContext,
		Diagnostics:     diagnostics,
		Outcomes:        outcomes,
		Summary: jsonSummary{
			Total:        summary.Total,
			Holds:        summary.Holds,
			Violated:     summary.Violated,
			Inconclusive: summary.Inconclusive,
			Errors:       summary.Errors,
		},
	}
}

func makeJSONResult(result verify.Result) *jsonResult {
	evidence := make([]jsonEvidence, 0, len(result.Evidence))
	for _, item := range result.Evidence {
		evidence = append(evidence, jsonEvidence{
			File:      item.File,
			StartLine: item.StartLine,
			EndLine:   item.EndLine,
			Reason:    item.Reason,
		})
	}
	return &jsonResult{
		Verdict:        result.Verdict,
		Summary:        result.Summary,
		Evidence:       evidence,
		Counterexample: result.Counterexample,
	}
}

func operationalError(outcome verify.Outcome) string {
	if outcome.Error != nil {
		return outcome.Error.Error()
	}
	if outcome.Result == nil {
		return "missing validation result"
	}
	if err := outcome.Result.Validate(); err != nil {
		return "invalid validation result: " + err.Error()
	}
	return ""
}

func durationMilliseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

// ResumeCommand returns the copyable CLI command for a supported agent session.
// It returns an empty string when either value is unavailable or the agent is not
// supported, and shell-quotes unusual session IDs before displaying them.
func ResumeCommand(agentName, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	argument := quoteShellArgument(sessionID)
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case "codex":
		return "codex resume " + argument
	case "claude":
		return "claude --resume " + argument
	default:
		return ""
	}
}

func quoteShellArgument(value string) string {
	if value != "" && strings.IndexFunc(value, func(character rune) bool {
		return !isShellSafe(character)
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func isShellSafe(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		strings.ContainsRune("_@%+=:,./-", character)
}
