package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Chriscbr/precept/internal/prompt"
)

type claudeRunner struct {
	executable string
}

func (runner *claudeRunner) Name() string {
	return "claude"
}

func (runner *claudeRunner) Preflight(ctx context.Context) (Info, error) {
	return preflight(ctx, runner.Name(), runner.executable)
}

func (runner *claudeRunner) Run(ctx context.Context, request Request) (RunResult, error) {
	execution := &claudeExecution{runner: runner, ctx: ctx, request: request}
	execution.validateRequest()
	execution.resolveExecutable()
	execution.buildArguments()
	execution.runAgent()
	execution.decodeOutput()
	execution.findConversation()
	execution.checkExecution()
	execution.parseResult()
	return execution.result, execution.err
}

type claudeExecution struct {
	runner     *claudeRunner
	ctx        context.Context
	request    Request
	root       string
	executable string
	arguments  []string
	output     commandOutput
	runErr     error
	decoded    decodedOutput
	decodeErr  error
	result     RunResult
	err        error
}

func (execution *claudeExecution) validateRequest() {
	execution.root, execution.err = validateRequest(execution.request)
	if execution.err != nil {
		execution.err = fmt.Errorf("run %s: %w", execution.runner.Name(), execution.err)
	}
}

func (execution *claudeExecution) resolveExecutable() {
	if execution.err == nil {
		execution.executable, execution.err = resolveExecutable(
			execution.runner.Name(),
			execution.runner.executable,
		)
	}
}

func (execution *claudeExecution) buildArguments() {
	if execution.err != nil {
		return
	}
	execution.arguments = []string{
		"--print",
		"--output-format", "json",
		"--json-schema", string(prompt.SchemaBytes()),
		"--safe-mode",
		"--no-chrome",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--disable-slash-commands",
		"--permission-mode", "dontAsk",
		"--tools", "Read,Grep,Glob",
	}
	if execution.request.Model != "" {
		execution.arguments = append(execution.arguments, "--model", execution.request.Model)
	}
	if execution.request.Effort != "" {
		execution.arguments = append(execution.arguments, "--effort", execution.request.Effort)
	}
}

func (execution *claudeExecution) runAgent() {
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

func (execution *claudeExecution) decodeOutput() {
	if execution.err == nil {
		execution.decoded, execution.decodeErr = decodeClaudeOutput(execution.output.stdout)
		execution.result.SessionID = execution.decoded.sessionID
	}
}

func (execution *claudeExecution) findConversation() {
	if execution.err == nil {
		execution.result.ConversationPath = findClaudeConversationPath(
			execution.root,
			execution.decoded.sessionID,
		)
	}
}

func (execution *claudeExecution) checkExecution() {
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

func (execution *claudeExecution) parseResult() {
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

func decodeClaudeOutput(output []byte) (decodedOutput, error) {
	decoder := &claudeOutputDecoder{output: output}
	decoder.decodeEnvelope()
	decoder.decodeSessionID()
	decoder.captureDirectResult()
	decoder.captureAgentError()
	decoder.capturePayload()
	return decoder.decoded, decoder.err
}

type claudeOutputDecoder struct {
	output  []byte
	fields  map[string]json.RawMessage
	decoded decodedOutput
	err     error
	done    bool
}

func (decoder *claudeOutputDecoder) decodeEnvelope() {
	decoder.err = json.Unmarshal(decoder.output, &decoder.fields)
	if decoder.err != nil {
		decoder.err = fmt.Errorf("decode JSON output envelope: %w", decoder.err)
	}
}

func (decoder *claudeOutputDecoder) decodeSessionID() {
	if decoder.err != nil {
		return
	}
	rawSessionID, exists := decoder.fields["session_id"]
	if !exists {
		return
	}
	decoder.err = json.Unmarshal(rawSessionID, &decoder.decoded.sessionID)
	if decoder.err != nil {
		decoder.err = fmt.Errorf("decode JSON output envelope session_id: %w", decoder.err)
		return
	}
	decoder.decoded.sessionID = strings.TrimSpace(decoder.decoded.sessionID)
}

func (decoder *claudeOutputDecoder) captureDirectResult() {
	if decoder.err != nil {
		return
	}
	if _, directResult := decoder.fields["verdict"]; directResult {
		decoder.decoded.payload = bytes.TrimSpace(decoder.output)
		decoder.done = true
	}
}

func (decoder *claudeOutputDecoder) captureAgentError() {
	if decoder.err != nil || decoder.done {
		return
	}
	rawError, exists := decoder.fields["is_error"]
	if !exists {
		return
	}
	var isError bool
	decoder.err = json.Unmarshal(rawError, &isError)
	if decoder.err != nil {
		decoder.err = fmt.Errorf("decode JSON output envelope is_error: %w", decoder.err)
		return
	}
	if isError {
		detail := claudeEnvelopeText(decoder.fields["result"])
		if detail == "" {
			detail = "agent returned an error result"
		}
		decoder.err = fmt.Errorf("agent error: %s", detail)
	}
}

func (decoder *claudeOutputDecoder) capturePayload() {
	if decoder.err != nil || decoder.done {
		return
	}
	structured, hasStructured := decoder.fields["structured_output"]
	structuredIsNull := bytes.Equal(bytes.TrimSpace(structured), []byte("null"))
	if hasStructured && !structuredIsNull {
		decoder.decoded.payload, decoder.err = decodeEnvelopePayload("structured_output", structured)
		return
	}
	result, hasResult := decoder.fields["result"]
	if hasResult {
		decoder.decoded.payload, decoder.err = decodeEnvelopePayload("result", result)
		return
	}
	decoder.err = fmt.Errorf("JSON output envelope did not contain structured_output or result")
}

func decodeEnvelopePayload(field string, raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	var payload []byte
	var err error
	switch {
	case len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")):
		err = fmt.Errorf("JSON output envelope field %s was empty", field)
	case trimmed[0] != '"':
		payload = trimmed
	default:
		var text string
		err = json.Unmarshal(trimmed, &text)
		if err != nil {
			err = fmt.Errorf("decode JSON output envelope field %s: %w", field, err)
		} else if len(bytes.TrimSpace([]byte(text))) == 0 {
			err = fmt.Errorf("JSON output envelope field %s was empty", field)
		} else {
			payload = []byte(text)
		}
	}
	return payload, err
}

func claudeEnvelopeText(raw json.RawMessage) string {
	payload, err := decodeEnvelopePayload("result", raw)
	if err != nil {
		return ""
	}
	return string(payload)
}
