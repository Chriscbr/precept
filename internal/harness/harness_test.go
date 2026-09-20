package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Chriscbr/precept/internal/prompt"
)

const validResultJSON = `{"verdict":"holds","summary":"the bound is applied","evidence":[{"file":"src/value.go","start_line":12,"end_line":14,"reason":"the return applies the bound"}],"counterexample":""}`

func TestNew(t *testing.T) {
	tests := []struct {
		name      string
		wantName  string
		wantError string
	}{
		{name: "claude", wantName: "claude"},
		{name: " CODEX ", wantName: "codex"},
		{name: "opencode", wantError: "supported harnesses: claude, codex"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(test.name)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("New() error = %v, want error containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if runner.Name() != test.wantName {
				t.Fatalf("Name() = %q, want %q", runner.Name(), test.wantName)
			}
		})
	}
}

func TestPreflight(t *testing.T) {
	executable := writeFakeExecutable(t)
	t.Setenv("PRECEPT_FAKE_VERSION", "fake-agent 1.2.3")
	runner := &claudeRunner{executable: executable}

	info, err := runner.Preflight(context.Background())
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if info.Name != "claude" || info.Version != "" {
		t.Fatalf("Preflight() info = %+v", info)
	}
	wantExecutable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Executable != wantExecutable {
		t.Errorf("Executable = %q, want %q", info.Executable, wantExecutable)
	}
}

func TestPreflightMissingExecutable(t *testing.T) {
	runner := &codexRunner{executable: filepath.Join(t.TempDir(), "missing-codex")}
	_, err := runner.Preflight(context.Background())
	if err == nil || !strings.Contains(err.Error(), "preflight codex") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Preflight() error = %v", err)
	}
}

func TestClaudeRun(t *testing.T) {
	executable := writeFakeExecutable(t)
	repositoryRoot := t.TempDir()
	captureDirectory := t.TempDir()
	claudeConfig := t.TempDir()
	conversationPath := filepath.Join(claudeConfig, "projects", "project-a", "claude-session.jsonl")
	writeTestFile(t, conversationPath, "conversation")
	argsPath := filepath.Join(captureDirectory, "args")
	stdinPath := filepath.Join(captureDirectory, "stdin")
	cwdPath := filepath.Join(captureDirectory, "cwd")
	inlineSchemaPath := filepath.Join(captureDirectory, "inline-schema.json")
	t.Setenv("PRECEPT_FAKE_ARGS", argsPath)
	t.Setenv("PRECEPT_FAKE_STDIN", stdinPath)
	t.Setenv("PRECEPT_FAKE_CWD", cwdPath)
	t.Setenv("PRECEPT_FAKE_INLINE_SCHEMA", inlineSchemaPath)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfig)
	rawStdout := `{"type":"result","subtype":"success","is_error":false,"session_id":"claude-session","structured_output":` + validResultJSON + `}`
	rawStderr := "claude diagnostic"
	t.Setenv("PRECEPT_FAKE_STDOUT", rawStdout)
	t.Setenv("PRECEPT_FAKE_STDERR", rawStderr)

	runner := &claudeRunner{executable: executable}
	result, err := runner.Run(context.Background(), Request{
		RepositoryRoot: repositoryRoot,
		Prompt:         "verify this claim",
		Model:          "sonnet",
		Effort:         "high",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Result.Verdict != prompt.VerdictHolds {
		t.Fatalf("Run() result = %+v", result)
	}
	if result.SessionID != "claude-session" {
		t.Errorf("SessionID = %q, want claude-session", result.SessionID)
	}
	if result.ConversationPath != conversationPath {
		t.Errorf("ConversationPath = %q, want %q", result.ConversationPath, conversationPath)
	}
	args := readLines(t, argsPath)
	assertArgumentPair(t, args, "--output-format", "json")
	assertArgumentPair(t, args, "--model", "sonnet")
	assertArgumentPair(t, args, "--effort", "high")
	assertArgumentPair(t, args, "--permission-mode", "dontAsk")
	assertArgumentPair(t, args, "--tools", "Read,Grep,Glob")
	assertArgumentPair(t, args, "--mcp-config", `{"mcpServers":{}}`)
	for _, flag := range []string{"--print", "--safe-mode", "--no-chrome", "--strict-mcp-config", "--disable-slash-commands"} {
		assertArgument(t, args, flag)
	}
	assertNoArgument(t, args, "--no-session-persistence")
	if argumentIndex(args, "--json-schema") < 0 || readFile(t, inlineSchemaPath) != string(prompt.SchemaBytes()) {
		t.Fatal("Claude did not receive the embedded schema inline")
	}
	if got := readFile(t, stdinPath); got != "verify this claim" {
		t.Errorf("stdin = %q", got)
	}
	wantRoot, _ := filepath.EvalSymlinks(repositoryRoot)
	if got := strings.TrimSpace(readFile(t, cwdPath)); got != wantRoot {
		t.Errorf("cwd = %q, want %q", got, wantRoot)
	}
	assertDirectoryEmpty(t, repositoryRoot)
}

func TestCodexRun(t *testing.T) {
	executable := writeFakeExecutable(t)
	repositoryRoot := t.TempDir()
	captureDirectory := t.TempDir()
	codexHome := t.TempDir()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.FixedZone("local", -4*60*60))
	conversationPath := filepath.Join(codexHome, "sessions", "2026", "07", "21", "rollout-2026-07-21T12-00-00-codex-thread.jsonl")
	writeTestFile(t, conversationPath, "conversation")
	argsPath := filepath.Join(captureDirectory, "args")
	stdinPath := filepath.Join(captureDirectory, "stdin")
	schemaCopyPath := filepath.Join(captureDirectory, "schema.json")
	t.Setenv("PRECEPT_FAKE_ARGS", argsPath)
	t.Setenv("PRECEPT_FAKE_STDIN", stdinPath)
	t.Setenv("PRECEPT_FAKE_SCHEMA_COPY", schemaCopyPath)
	t.Setenv("CODEX_HOME", codexHome)
	rawStdout := "{\"type\":\"thread.started\",\"thread_id\":\"codex-thread\"}\n" + codexAgentMessage(validResultJSON) + "\n{\"type\":\"turn.completed\"}\n"
	rawStderr := "codex diagnostic"
	t.Setenv("PRECEPT_FAKE_STDOUT", rawStdout)
	t.Setenv("PRECEPT_FAKE_STDERR", rawStderr)

	runner := &codexRunner{executable: executable, now: func() time.Time { return now }}
	result, err := runner.Run(context.Background(), Request{
		RepositoryRoot: repositoryRoot,
		Prompt:         "verify codex claim",
		Model:          "gpt-5.4-mini",
		Effort:         "medium",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Result.Verdict != prompt.VerdictHolds {
		t.Fatalf("Run() result = %+v", result)
	}
	if result.SessionID != "codex-thread" {
		t.Errorf("SessionID = %q, want codex-thread", result.SessionID)
	}
	if result.ConversationPath != conversationPath {
		t.Errorf("ConversationPath = %q, want %q", result.ConversationPath, conversationPath)
	}
	args := readLines(t, argsPath)
	wantPrefix := []string{
		"--ask-for-approval", "never",
		"--model", "gpt-5.4-mini",
		"-c", "model_reasoning_effort=medium",
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
		"exec",
	}
	if len(args) < len(wantPrefix) || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("Codex argument prefix = %#v, want %#v", args, wantPrefix)
	}
	for _, flag := range []string{"--ignore-user-config", "--ignore-rules", "--json"} {
		assertArgument(t, args, flag)
	}
	assertNoArgument(t, args, "--ephemeral")
	assertArgumentPair(t, args, "--sandbox", "read-only")
	assertArgumentPair(t, args, "--color", "never")
	assertArgumentPair(t, args, "--cd", canonicalPath(t, repositoryRoot))
	schemaIndex := argumentIndex(args, "--output-schema")
	if schemaIndex < 0 || schemaIndex+1 >= len(args) {
		t.Fatal("Codex did not receive --output-schema")
	}
	schemaPath := args[schemaIndex+1]
	if pathInside(canonicalPath(t, repositoryRoot), schemaPath) {
		t.Fatalf("schema path %q is inside repository %q", schemaPath, repositoryRoot)
	}
	if _, err := os.Stat(schemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary schema still exists after Run(): %v", err)
	}
	if got := readFile(t, schemaCopyPath); got != string(prompt.SchemaBytes()) {
		t.Fatal("temporary Codex schema did not match embedded schema")
	}
	if got := readFile(t, stdinPath); got != "verify codex claim" {
		t.Errorf("stdin = %q", got)
	}
	if args[len(args)-1] != "-" {
		t.Errorf("last argument = %q, want stdin marker", args[len(args)-1])
	}
	assertDirectoryEmpty(t, repositoryRoot)
}

func TestRunReportsAgentFailure(t *testing.T) {
	executable := writeFakeExecutable(t)
	claudeConfig := t.TempDir()
	conversationPath := filepath.Join(claudeConfig, "projects", "project-a", "failed-session.jsonl")
	writeTestFile(t, conversationPath, "conversation")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfig)
	rawStdout := `{"type":"result","is_error":true,"session_id":"failed-session","result":"agent failed"}`
	t.Setenv("PRECEPT_FAKE_STDOUT", rawStdout)
	t.Setenv("PRECEPT_FAKE_STDERR", "authentication required")
	t.Setenv("PRECEPT_FAKE_EXIT_CODE", "17")
	runner := &claudeRunner{executable: executable}
	result, err := runner.Run(context.Background(), Request{RepositoryRoot: t.TempDir(), Prompt: "prompt"})
	if err == nil || !strings.Contains(err.Error(), "run claude failed") || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.SessionID != "failed-session" || result.ConversationPath != conversationPath {
		t.Fatalf("Run() did not preserve failure metadata: %+v", result)
	}
}

func TestCodexRunReportsConversationOnFailure(t *testing.T) {
	executable := writeFakeExecutable(t)
	codexHome := t.TempDir()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	conversationPath := filepath.Join(codexHome, "sessions", "2026", "07", "21", "rollout-failure-codex-failed.jsonl")
	writeTestFile(t, conversationPath, "conversation")
	t.Setenv("CODEX_HOME", codexHome)
	rawStdout := "{\"type\":\"thread.started\",\"thread_id\":\"codex-failed\"}\n{\"type\":\"error\",\"message\":\"agent failed\"}\n"
	t.Setenv("PRECEPT_FAKE_STDOUT", rawStdout)
	t.Setenv("PRECEPT_FAKE_STDERR", "authentication required")
	t.Setenv("PRECEPT_FAKE_EXIT_CODE", "17")

	runner := &codexRunner{executable: executable, now: func() time.Time { return now }}
	result, err := runner.Run(context.Background(), Request{RepositoryRoot: t.TempDir(), Prompt: "prompt"})
	if err == nil || !strings.Contains(err.Error(), "run codex failed") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.SessionID != "codex-failed" || result.ConversationPath != conversationPath {
		t.Fatalf("Run() did not preserve Codex failure metadata: %+v", result)
	}
}

func TestFindClaudeConversationPath(t *testing.T) {
	t.Run("configured root", func(t *testing.T) {
		configRoot := t.TempDir()
		want := filepath.Join(configRoot, "projects", "project-b", "session-123.jsonl")
		writeTestFile(t, want, "conversation")
		t.Setenv("CLAUDE_CONFIG_DIR", configRoot)
		if got := findClaudeConversationPath(t.TempDir(), "session-123"); got != want {
			t.Fatalf("findClaudeConversationPath() = %q, want %q", got, want)
		}
	})

	t.Run("home fallback", func(t *testing.T) {
		home := t.TempDir()
		want := filepath.Join(home, ".claude", "projects", "project-a", "fallback-session.jsonl")
		writeTestFile(t, want, "conversation")
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("HOME", home)
		if got := findClaudeConversationPath(t.TempDir(), "fallback-session"); got != want {
			t.Fatalf("findClaudeConversationPath() = %q, want %q", got, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		if got := findClaudeConversationPath(t.TempDir(), "missing-session"); got != "" {
			t.Fatalf("findClaudeConversationPath() = %q, want empty", got)
		}
	})

	t.Run("relative configured root", func(t *testing.T) {
		repositoryRoot := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", "relative-claude-config")
		want := filepath.Join(repositoryRoot, "relative-claude-config", "projects", "project-a", "relative-session.jsonl")
		writeTestFile(t, want, "conversation")
		if got := findClaudeConversationPath(repositoryRoot, "relative-session"); got != want {
			t.Fatalf("findClaudeConversationPath() = %q, want %q", got, want)
		}
	})
}

func TestFindCodexConversationPathDates(t *testing.T) {
	now := time.Date(2026, time.January, 1, 23, 30, 0, 0, time.FixedZone("local", -5*60*60))
	tests := []struct {
		name     string
		datePath string
	}{
		{name: "current local", datePath: "2026/01/01"},
		{name: "current UTC", datePath: "2026/01/02"},
		{name: "adjacent previous", datePath: "2025/12/31"},
		{name: "adjacent next", datePath: "2026/01/03"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			codexHome := t.TempDir()
			t.Setenv("CODEX_HOME", codexHome)
			want := filepath.Join(codexHome, "sessions", filepath.FromSlash(test.datePath), "rollout-2026-01-01T00-00-00-thread-123.jsonl")
			writeTestFile(t, want, "conversation")
			if got := findCodexConversationPathAt(t.TempDir(), "thread-123", now); got != want {
				t.Fatalf("findCodexConversationPathAt() = %q, want %q", got, want)
			}
		})
	}
}

func TestFindCodexConversationPathUsesHomeFallback(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	want := filepath.Join(home, ".codex", "sessions", "2026", "07", "21", "rollout-now-fallback-thread.jsonl")
	writeTestFile(t, want, "conversation")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)
	if got := findCodexConversationPathAt(t.TempDir(), "fallback-thread", now); got != want {
		t.Fatalf("findCodexConversationPathAt() = %q, want %q", got, want)
	}
}

func TestFindCodexConversationPathUsesRelativeConfiguredRoot(t *testing.T) {
	repositoryRoot := t.TempDir()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	t.Setenv("CODEX_HOME", "relative-codex-home")
	want := filepath.Join(repositoryRoot, "relative-codex-home", "sessions", "2026", "07", "21", "rollout-now-relative-thread.jsonl")
	writeTestFile(t, want, "conversation")
	if got := findCodexConversationPathAt(repositoryRoot, "relative-thread", now); got != want {
		t.Fatalf("findCodexConversationPathAt() = %q, want %q", got, want)
	}
}

func TestConversationPathRejectsUnsafeSessionIDs(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	repositoryRoot := t.TempDir()
	for _, sessionID := range []string{"", ".", "..", "../escape", "nested/session", `nested\session`, "session*", "session?", "session[1]", "session id", "セッション"} {
		t.Run(sessionID, func(t *testing.T) {
			if got := findClaudeConversationPath(repositoryRoot, sessionID); got != "" {
				t.Errorf("findClaudeConversationPath(%q) = %q", sessionID, got)
			}
			if got := findCodexConversationPathAt(repositoryRoot, sessionID, now); got != "" {
				t.Errorf("findCodexConversationPathAt(%q) = %q", sessionID, got)
			}
		})
	}
}

func TestRunReportsCancellation(t *testing.T) {
	executable := writeFakeExecutable(t)
	context, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &codexRunner{executable: executable}
	_, err := runner.Run(context, Request{RepositoryRoot: t.TempDir(), Prompt: "prompt"})
	if err == nil || !strings.Contains(err.Error(), "run codex canceled") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestBoundedBuffer(t *testing.T) {
	buffer := &boundedBuffer{limit: 4}
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil || written != 6 {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if got := string(buffer.bytes()); got != "abcd" || !buffer.truncated {
		t.Fatalf("buffer = %q, truncated = %v", got, buffer.truncated)
	}
}

func TestBoundedErrorDetailKeepsBothEnds(t *testing.T) {
	detail, truncated := boundedErrorDetail([]byte("abcdefghij"), 6)
	if !truncated || detail != "abc\n...\nhij" {
		t.Fatalf("boundedErrorDetail() = %q, %v", detail, truncated)
	}
}

func TestValidateRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		request Request
		want    string
	}{
		{name: "empty root", request: Request{Prompt: "prompt"}, want: "repository root"},
		{name: "empty prompt", request: Request{RepositoryRoot: t.TempDir()}, want: "prompt"},
		{name: "root is file", request: Request{RepositoryRoot: file, Prompt: "prompt"}, want: "not a directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateRequest(test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPreflightHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&claudeRunner{executable: writeFakeExecutable(t)}).Preflight(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Preflight() error = %v", err)
	}
}

func TestPreflightDoesNotLaunchAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	marker := filepath.Join(t.TempDir(), "launched")
	t.Setenv("PRECEPT_PREFLIGHT_MARKER", marker)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n: > \"$PRECEPT_PREFLIGHT_MARKER\"\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (&codexRunner{executable: path}).Preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight launched agent: %v", err)
	}
}

func writeFakeExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  if [ -n "$PRECEPT_FAKE_VERSION_DELAY" ]; then
    sleep "$PRECEPT_FAKE_VERSION_DELAY"
  fi
  printf '%s' "${PRECEPT_FAKE_VERSION:-fake-agent 0.0.0}"
  exit "${PRECEPT_FAKE_EXIT_CODE:-0}"
fi
if [ -n "$PRECEPT_FAKE_ARGS" ]; then
  : > "$PRECEPT_FAKE_ARGS"
fi
want_schema=0
want_inline_schema=0
for argument in "$@"; do
  if [ -n "$PRECEPT_FAKE_ARGS" ]; then
    printf '%s\n' "$argument" >> "$PRECEPT_FAKE_ARGS"
  fi
  if [ "$want_schema" = "1" ] && [ -n "$PRECEPT_FAKE_SCHEMA_COPY" ]; then
    cp "$argument" "$PRECEPT_FAKE_SCHEMA_COPY"
    want_schema=0
  fi
  if [ "$want_inline_schema" = "1" ] && [ -n "$PRECEPT_FAKE_INLINE_SCHEMA" ]; then
    printf '%s' "$argument" > "$PRECEPT_FAKE_INLINE_SCHEMA"
    want_inline_schema=0
  fi
  if [ "$argument" = "--output-schema" ]; then
    want_schema=1
  fi
  if [ "$argument" = "--json-schema" ]; then
    want_inline_schema=1
  fi
done
if [ -n "$PRECEPT_FAKE_STDIN" ]; then
  cat > "$PRECEPT_FAKE_STDIN"
else
  cat > /dev/null
fi
if [ -n "$PRECEPT_FAKE_CWD" ]; then
  pwd > "$PRECEPT_FAKE_CWD"
fi
printf '%s' "$PRECEPT_FAKE_STDOUT"
printf '%s' "$PRECEPT_FAKE_STDERR" >&2
exit "${PRECEPT_FAKE_EXIT_CODE:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake executable: %v", err)
	}
	return path
}

func codexAgentMessage(message string) string {
	return `{"type":"item.completed","item":{"type":"agent_message","text":` + strconv.Quote(message) + `}}`
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	text := strings.TrimSuffix(readFile(t, path), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertArgument(t *testing.T, args []string, want string) {
	t.Helper()
	if argumentIndex(args, want) < 0 {
		t.Errorf("arguments %#v do not contain %q", args, want)
	}
}

func assertNoArgument(t *testing.T, args []string, unwanted string) {
	t.Helper()
	if argumentIndex(args, unwanted) >= 0 {
		t.Errorf("arguments %#v unexpectedly contain %q", args, unwanted)
	}
}

func assertArgumentPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	index := argumentIndex(args, flag)
	if index < 0 || index+1 >= len(args) || args[index+1] != value {
		t.Errorf("arguments %#v do not contain %q followed by %q", args, flag, value)
	}
}

func argumentIndex(args []string, want string) int {
	for index, argument := range args {
		if argument == want {
			return index
		}
	}
	return -1
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q contains unexpected entries: %+v", path, entries)
	}
}
