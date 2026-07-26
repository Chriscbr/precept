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

	"precept/internal/prompt"
)

type codexRunner struct {
	executable string
	now        func() time.Time
}

func (runner *codexRunner) Name() string {
	return "codex"
}

func (runner *codexRunner) Preflight(ctx context.Context) (Info, error) {
	return preflight(ctx, runner.Name(), runner.executable)
}

func (runner *codexRunner) Run(ctx context.Context, request Request) (RunResult, error) {
	root, err := validateRequest(request)
	if err != nil {
		return RunResult{}, fmt.Errorf("run %s: %w", runner.Name(), err)
	}
	executable, err := resolveExecutable(runner.Name(), runner.executable)
	if err != nil {
		return RunResult{}, err
	}

	temporaryDirectory, err := makeTempDirOutside(root, "precept-codex-")
	if err != nil {
		return RunResult{}, fmt.Errorf("run %s: create temporary schema directory: %w", runner.Name(), err)
	}
	defer func() {
		_ = os.RemoveAll(temporaryDirectory)
	}()
	schemaPath := filepath.Join(temporaryDirectory, "result.schema.json")
	if err := os.WriteFile(schemaPath, prompt.SchemaBytes(), 0o600); err != nil {
		return RunResult{}, fmt.Errorf("run %s: write temporary result schema: %w", runner.Name(), err)
	}

	// --ask-for-approval is a global option and must precede the exec
	// subcommand. Model and effort are global as well, which keeps their
	// placement compatible with current Codex CLI releases.
	args := []string{"--ask-for-approval", "never"}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	if request.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+request.Effort)
	}
	// Repository AGENTS.md files are source-controlled evidence for this task,
	// not trusted verifier instructions. Keep them out of Codex's higher-
	// priority project-instructions prompt, and disable Codex's otherwise-
	// default cached web-search tool.
	args = append(args,
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
	)
	args = append(args,
		"exec",
		"--ignore-user-config",
		"--ignore-rules",
		"--sandbox", "read-only",
		"--cd", root,
		"--output-schema", schemaPath,
		"--color", "never",
		"--json",
		"-",
	)

	output, runErr := execute(ctx, executable, args, root, request.Prompt, maxAgentOutputBytes)
	runResult := RunResult{}
	decoded, decodeErr := decodeCodexOutput(output.stdout)
	runResult.SessionID = decoded.sessionID
	if runner.now == nil {
		runResult.ConversationPath = findCodexConversationPath(root, decoded.sessionID)
	} else {
		runResult.ConversationPath = findCodexConversationPathAt(root, decoded.sessionID, runner.now())
	}
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
	var decoded decodedOutput
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), maxAgentOutputBytes)
	var finalMessage []byte
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var event codexEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return decoded, fmt.Errorf("decode JSONL event on line %d: %w", lineNumber, err)
		}
		if event.Type == "thread.started" {
			if sessionID := strings.TrimSpace(event.ThreadID); sessionID != "" {
				decoded.sessionID = sessionID
			}
		}
		if event.Type == "error" {
			detail := strings.TrimSpace(event.Message)
			if detail == "" {
				detail = "agent emitted an error event"
			}
			return decoded, fmt.Errorf("agent error: %s", detail)
		}

		if event.Type == "item.completed" && event.Item != nil && event.Item.Type == "agent_message" {
			message, err := codexItemText(*event.Item)
			if err != nil {
				return decoded, fmt.Errorf("decode final assistant message on line %d: %w", lineNumber, err)
			}
			if len(bytes.TrimSpace(message)) > 0 {
				finalMessage = bytes.Clone(message)
			}
			continue
		}

		// Some older JSONL variants emit an agent_message directly rather than
		// wrapping it in item.completed.
		if event.Type == "agent_message" {
			message, err := rawText(event.Text)
			if err != nil {
				return decoded, fmt.Errorf("decode final assistant message on line %d: %w", lineNumber, err)
			}
			if len(bytes.TrimSpace(message)) > 0 {
				finalMessage = bytes.Clone(message)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return decoded, fmt.Errorf("scan JSONL output: %w", err)
	}
	if len(finalMessage) == 0 {
		return decoded, fmt.Errorf("JSONL output did not contain a final assistant message")
	}
	decoded.payload = finalMessage
	return decoded, nil
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
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] != '"' {
		return bytes.Clone(trimmed), nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return nil, err
	}
	return []byte(text), nil
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
