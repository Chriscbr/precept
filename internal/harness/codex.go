package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Chriscbr/precept/internal/prompt"
)

type codexRunner struct {
	executable string
	now        func() time.Time
}

const schemaFileMode = 0o600

func (runner *codexRunner) Name() string {
	return "codex"
}

func (runner *codexRunner) Preflight(ctx context.Context) (Info, error) {
	return preflight(ctx, runner.Name(), runner.executable)
}

func (runner *codexRunner) Run(ctx context.Context, request Request) (RunResult, error) {
	execution := &codexExecution{runner: runner, ctx: ctx, request: request}
	defer execution.cleanup()
	execution.validateRequest()
	execution.resolveExecutable()
	execution.createSchemaDirectory()
	execution.writeSchema()
	execution.buildArguments()
	execution.runAgent()
	execution.decodeOutput()
	execution.findConversation()
	execution.checkExecution()
	execution.parseResult()
	return execution.result, execution.err
}

type codexExecution struct {
	runner             *codexRunner
	ctx                context.Context
	request            Request
	root               string
	executable         string
	temporaryDirectory string
	schemaPath         string
	arguments          []string
	output             commandOutput
	runErr             error
	decoded            decodedOutput
	decodeErr          error
	result             RunResult
	err                error
}

func (execution *codexExecution) validateRequest() {
	execution.root, execution.err = validateRequest(execution.request)
	if execution.err != nil {
		execution.err = fmt.Errorf("run %s: %w", execution.runner.Name(), execution.err)
	}
}

func (execution *codexExecution) resolveExecutable() {
	if execution.err == nil {
		execution.executable, execution.err = resolveExecutable(
			execution.runner.Name(),
			execution.runner.executable,
		)
	}
}

func (execution *codexExecution) createSchemaDirectory() {
	if execution.err != nil {
		return
	}
	execution.temporaryDirectory, execution.err = makeTempDirOutside(execution.root, "precept-codex-")
	if execution.err != nil {
		execution.err = fmt.Errorf(
			"run %s: create temporary schema directory: %w",
			execution.runner.Name(),
			execution.err,
		)
	}
}

func (execution *codexExecution) cleanup() {
	if execution.temporaryDirectory != "" {
		_ = os.RemoveAll(execution.temporaryDirectory)
	}
}

func (execution *codexExecution) writeSchema() {
	if execution.err != nil {
		return
	}
	execution.schemaPath = filepath.Join(execution.temporaryDirectory, "result.schema.json")
	execution.err = os.WriteFile(execution.schemaPath, prompt.SchemaBytes(), schemaFileMode)
	if execution.err != nil {
		execution.err = fmt.Errorf(
			"run %s: write temporary result schema: %w",
			execution.runner.Name(),
			execution.err,
		)
	}
}

func (execution *codexExecution) buildArguments() {
	if execution.err != nil {
		return
	}
	// --ask-for-approval is a global option and must precede the exec
	// subcommand. Model and effort are global as well, which keeps their
	// placement compatible with current Codex CLI releases.
	execution.arguments = []string{"--ask-for-approval", "never"}
	if execution.request.Model != "" {
		execution.arguments = append(execution.arguments, "--model", execution.request.Model)
	}
	if execution.request.Effort != "" {
		execution.arguments = append(
			execution.arguments,
			"-c",
			"model_reasoning_effort="+execution.request.Effort,
		)
	}
	// Repository AGENTS.md files are source-controlled evidence for this task,
	// not trusted verifier instructions. Keep them out of Codex's higher-
	// priority project-instructions prompt, and disable Codex's otherwise-
	// default cached web-search tool.
	execution.arguments = append(execution.arguments,
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
		"exec",
		"--ignore-user-config",
		"--ignore-rules",
		"--sandbox", "read-only",
		"--cd", execution.root,
		"--output-schema", execution.schemaPath,
		"--color", "never",
		"--json",
		"-",
	)
}

func (execution *codexExecution) runAgent() {
	if execution.err != nil {
		return
	}
	execution.output, execution.runErr = execute(
		execution.ctx,
		execution.executable,
		execution.arguments,
		execution.root,
		execution.request.Prompt,
		maxAgentOutputBytes,
	)
}

func (execution *codexExecution) decodeOutput() {
	if execution.err == nil {
		execution.decoded, execution.decodeErr = decodeCodexOutput(execution.output.stdout)
		execution.result.SessionID = execution.decoded.sessionID
	}
}

func (execution *codexExecution) findConversation() {
	if execution.err != nil {
		return
	}
	if execution.runner.now == nil {
		execution.result.ConversationPath = findCodexConversationPath(
			execution.root,
			execution.decoded.sessionID,
		)
		return
	}
	execution.result.ConversationPath = findCodexConversationPathAt(
		execution.root,
		execution.decoded.sessionID,
		execution.runner.now(),
	)
}

func (execution *codexExecution) checkExecution() {
	if execution.err != nil {
		return
	}
	truncationErr := rejectTruncatedOutput(execution.runner.Name(), execution.output)
	switch {
	case execution.runErr != nil:
		execution.err = processError(
			"run "+execution.runner.Name(),
			execution.runErr,
			execution.ctx.Err(),
			execution.output,
		)
	case truncationErr != nil:
		execution.err = truncationErr
	case execution.decodeErr != nil:
		execution.err = fmt.Errorf("run %s: %w", execution.runner.Name(), execution.decodeErr)
	}
}

func (execution *codexExecution) parseResult() {
	if execution.err != nil {
		return
	}
	execution.result.Result, execution.err = prompt.ParseResult(execution.decoded.payload)
	if execution.err != nil {
		execution.err = fmt.Errorf(
			"run %s: invalid final response: %w",
			execution.runner.Name(),
			execution.err,
		)
	}
}

type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Text     json.RawMessage `json:"text"`
	Message  string          `json:"message"`
	Item     *codexItem      `json:"item"`
}

type codexItem struct {
	Type    string          `json:"type"`
	Text    json.RawMessage `json:"text"`
	Content []codexContent  `json:"content"`
}

type codexContent struct {
	Type string          `json:"type"`
	Text json.RawMessage `json:"text"`
}

func decodeCodexOutput(output []byte) (decodedOutput, error) {
	decoder := &codexOutputDecoder{scanner: bufio.NewScanner(bytes.NewReader(output))}
	decoder.scanner.Buffer(make([]byte, 64<<10), maxAgentOutputBytes)
	decoder.decodeLines()
	decoder.finish()
	return decoder.decoded, decoder.err
}

type codexOutputDecoder struct {
	scanner      *bufio.Scanner
	decoded      decodedOutput
	finalMessage []byte
	lineNumber   int
	err          error
}

func (decoder *codexOutputDecoder) decodeLines() {
	for decoder.scanner.Scan() {
		decoder.lineNumber++
		decoder.decodeLine(decoder.scanner.Bytes())
		if decoder.err != nil {
			break
		}
	}
}

func (decoder *codexOutputDecoder) decodeLine(rawLine []byte) {
	line := bytes.TrimSpace(rawLine)
	if len(line) == 0 {
		return
	}
	var event codexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		decoder.err = fmt.Errorf("decode JSONL event on line %d: %w", decoder.lineNumber, err)
		return
	}
	decoder.captureSession(event)
	decoder.captureError(event)
	decoder.captureCompletedMessage(event)
	decoder.captureLegacyMessage(event)
}

func (decoder *codexOutputDecoder) captureSession(event codexEvent) {
	if event.Type != "thread.started" {
		return
	}
	if sessionID := strings.TrimSpace(event.ThreadID); sessionID != "" {
		decoder.decoded.sessionID = sessionID
	}
}

func (decoder *codexOutputDecoder) captureError(event codexEvent) {
	if decoder.err != nil || event.Type != "error" {
		return
	}
	detail := strings.TrimSpace(event.Message)
	if detail == "" {
		detail = "agent emitted an error event"
	}
	decoder.err = fmt.Errorf("agent error: %s", detail)
}

func (decoder *codexOutputDecoder) captureCompletedMessage(event codexEvent) {
	if decoder.err != nil || event.Type != "item.completed" || event.Item == nil {
		return
	}
	if event.Item.Type != "agent_message" {
		return
	}
	message, err := codexItemText(*event.Item)
	decoder.captureMessage(message, err)
}

func (decoder *codexOutputDecoder) captureLegacyMessage(event codexEvent) {
	// Some older JSONL variants emit an agent_message directly rather than
	// wrapping it in item.completed.
	if decoder.err != nil || event.Type != "agent_message" {
		return
	}
	message, err := rawText(event.Text)
	decoder.captureMessage(message, err)
}

func (decoder *codexOutputDecoder) captureMessage(message []byte, err error) {
	if err != nil {
		decoder.err = fmt.Errorf("decode final assistant message on line %d: %w", decoder.lineNumber, err)
		return
	}
	if len(bytes.TrimSpace(message)) > 0 {
		decoder.finalMessage = bytes.Clone(message)
	}
}

func (decoder *codexOutputDecoder) finish() {
	if decoder.err != nil {
		return
	}
	if err := decoder.scanner.Err(); err != nil {
		decoder.err = fmt.Errorf("scan JSONL output: %w", err)
		return
	}
	if len(decoder.finalMessage) == 0 {
		decoder.err = fmt.Errorf("JSONL output did not contain a final assistant message")
		return
	}
	decoder.decoded.payload = decoder.finalMessage
}

func codexItemText(item codexItem) ([]byte, error) {
	if len(bytes.TrimSpace(item.Text)) > 0 {
		return rawText(item.Text)
	}
	var combined []string
	for _, content := range item.Content {
		if content.Type != "output_text" && content.Type != "text" {
			continue
		}
		text, err := rawText(content.Text)
		if err != nil {
			return nil, err
		}
		if value := strings.TrimSpace(string(text)); value != "" {
			combined = append(combined, value)
		}
	}
	return []byte(strings.Join(combined, "\n")), nil
}

func rawText(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	var result []byte
	var err error
	switch {
	case len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")):
	case trimmed[0] != '"':
		result = bytes.Clone(trimmed)
	default:
		var value string
		err = json.Unmarshal(trimmed, &value)
		result = []byte(value)
	}
	return result, err
}

func makeTempDirOutside(repositoryRoot, pattern string) (string, error) {
	candidates := []string{os.TempDir(), "/tmp"}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		base, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if evaluated, evalErr := filepath.EvalSymlinks(base); evalErr == nil {
			base = evaluated
		}
		if _, duplicate := seen[base]; duplicate {
			continue
		}
		seen[base] = struct{}{}
		if pathInside(repositoryRoot, base) {
			continue
		}
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		directory, err := os.MkdirTemp(base, pattern)
		if err == nil {
			return directory, nil
		}
	}
	return "", fmt.Errorf("no writable OS temporary directory is available outside %q", repositoryRoot)
}

func pathInside(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
