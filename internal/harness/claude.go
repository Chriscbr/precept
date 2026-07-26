package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"precept/internal/prompt"
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
	root, err := validateRequest(request)
	if err != nil {
		return RunResult{}, fmt.Errorf("run %s: %w", runner.Name(), err)
	}
	executable, err := resolveExecutable(runner.Name(), runner.executable)
	if err != nil {
		return RunResult{}, err
	}

	args := []string{
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
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	if request.Effort != "" {
		args = append(args, "--effort", request.Effort)
	}

	output, runErr := execute(ctx, executable, args, root, request.Prompt, maxAgentOutputBytes)
	runResult := RunResult{}
	decoded, decodeErr := decodeClaudeOutput(output.stdout)
	runResult.SessionID = decoded.sessionID
	runResult.ConversationPath = findClaudeConversationPath(root, decoded.sessionID)
	if runErr != nil {
		return runResult, processError("run "+runner.Name(), runErr, ctx.Err(), output)
	}
	if err := rejectTruncatedOutput(runner.Name(), output); err != nil {
		return runResult, err
	}
	if decodeErr != nil {
		return runResult, fmt.Errorf("run %s: %w", runner.Name(), decodeErr)
	}
	result, err := prompt.ParseResult(decoded.payload)
	if err != nil {
		return runResult, fmt.Errorf("run %s: invalid final response: %w", runner.Name(), err)
	}
	runResult.Result = result
	return runResult, nil
}

func decodeClaudeOutput(output []byte) (decodedOutput, error) {
	var decoded decodedOutput
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output, &fields); err != nil {
		return decoded, fmt.Errorf("decode JSON output envelope: %w", err)
	}
	if rawSessionID, ok := fields["session_id"]; ok {
		if err := json.Unmarshal(rawSessionID, &decoded.sessionID); err != nil {
			return decoded, fmt.Errorf("decode JSON output envelope session_id: %w", err)
		}
		decoded.sessionID = string(bytes.TrimSpace([]byte(decoded.sessionID)))
	}
	if _, directResult := fields["verdict"]; directResult {
		decoded.payload = bytes.TrimSpace(output)
		return decoded, nil
	}

	if rawError, ok := fields["is_error"]; ok {
		var isError bool
		if err := json.Unmarshal(rawError, &isError); err != nil {
			return decoded, fmt.Errorf("decode JSON output envelope is_error: %w", err)
		}
		if isError {
			detail := claudeEnvelopeText(fields["result"])
			if detail == "" {
				detail = "agent returned an error result"
			}
			return decoded, fmt.Errorf("agent error: %s", detail)
		}
	}

	if structured, ok := fields["structured_output"]; ok && !bytes.Equal(bytes.TrimSpace(structured), []byte("null")) {
		payload, err := decodeEnvelopePayload("structured_output", structured)
		decoded.payload = payload
		return decoded, err
	}
	if result, ok := fields["result"]; ok {
		payload, err := decodeEnvelopePayload("result", result)
		decoded.payload = payload
		return decoded, err
	}
	return decoded, fmt.Errorf("JSON output envelope did not contain structured_output or result")
}

func decodeEnvelopePayload(field string, raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("JSON output envelope field %s was empty", field)
	}
	if trimmed[0] != '"' {
		return trimmed, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return nil, fmt.Errorf("decode JSON output envelope field %s: %w", field, err)
	}
	if len(bytes.TrimSpace([]byte(text))) == 0 {
		return nil, fmt.Errorf("JSON output envelope field %s was empty", field)
	}
	return []byte(text), nil
}

func claudeEnvelopeText(raw json.RawMessage) string {
	payload, err := decodeEnvelopePayload("result", raw)
	if err != nil {
		return ""
	}
	return string(payload)
}
