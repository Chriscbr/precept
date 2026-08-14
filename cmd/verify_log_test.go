package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/harness"
	"github.com/Chriscbr/precept/internal/report"
	"github.com/Chriscbr/precept/internal/verify"
)

func TestVerificationLogStoresConversationPathWithoutTranscript(t *testing.T) {
	t.Parallel()

	log, err := newVerificationLog(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := log.Path()
	t.Cleanup(func() {
		_ = log.Close()
		_ = os.Remove(path)
	})
	conversationPath := "/tmp/codex-conversation.jsonl"
	if err := log.WriteAgentSession("codex", discover.Claim{
		Marker: discover.MarkerInvariant, File: "fixture.go", MarkerLine: 3, Symbol: "Example",
	}, harness.RunResult{
		SessionID:        "session-123",
		ConversationPath: conversationPath,
	}, false); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, want := range []string{"session_id: session-123", "resume_command: codex resume session-123", "conversation_path: " + conversationPath} {
		if !strings.Contains(text, want) {
			t.Errorf("verification log does not contain %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"rendered prompt", "agent stdout", "agent stderr"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("verification log unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
}

func TestVerificationLogRedactsAgentErrorDetails(t *testing.T) {
	t.Parallel()

	log, err := newVerificationLog(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := log.Path()
	t.Cleanup(func() {
		_ = log.Close()
		_ = os.Remove(path)
	})

	const sensitive = "UNWANTED PROVIDER STDERR WITH SECRET"
	claim := discover.Claim{Marker: discover.MarkerInvariant, File: "fixture.go", MarkerLine: 3, Symbol: "Example"}
	if err := log.WriteAgentSession("codex", claim, harness.RunResult{SessionID: "session-123"}, true); err != nil {
		t.Fatal(err)
	}
	if err := log.WriteReport(report.Run{
		Agent:    report.Agent{Name: "codex"},
		Outcomes: []verify.Outcome{{Claim: claim, Error: errors.New(sensitive)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := log.WriteCommandExit(errors.New(sensitive)); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, sensitive) {
		t.Fatalf("verification log contains provider error detail:\n%s", text)
	}
	for _, want := range []string{
		"status: error",
		"validation failed; see CLI output and the agent conversation when available",
		"exit_code: 2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("verification log does not contain %q:\n%s", want, text)
		}
	}
}

func TestLogFailurePromotesSemanticFailureToOperational(t *testing.T) {
	t.Parallel()

	logErr := errors.New("log disk is full")
	err := promoteLogFailure(semanticFailure(), logErr)
	var typed *exitError
	if !errors.As(err, &typed) || typed.code != 2 {
		t.Fatalf("promoteLogFailure() = %#v, want exit code 2", err)
	}
	if !errors.Is(err, logErr) {
		t.Fatalf("promoteLogFailure() = %v, want wrapped log error", err)
	}
}

func TestVerificationLogIsPrivateAndTimestamped(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.July, 21, 15, 4, 5, 123456789, time.UTC)
	log, err := newVerificationLog(startedAt)
	if err != nil {
		t.Fatalf("newVerificationLog() error = %v", err)
	}
	path := log.Path()
	t.Cleanup(func() {
		_ = log.Close()
		_ = os.Remove(path)
	})

	if !strings.HasPrefix(filepath.Base(path), "precept-verify-20260721T150405.123456789Z-") {
		t.Fatalf("verification log name = %q", filepath.Base(path))
	}
	if directory := filepath.Dir(path); directory != "/tmp" {
		t.Fatalf("verification log directory = %q, want /tmp", directory)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat verification log: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("verification log permissions = %o, want 600", permissions)
	}

	if err := log.WriteSection("run", "scope: fixture.go\nagent: codex\n"); err != nil {
		t.Fatalf("WriteSection(run) error = %v", err)
	}
	if err := log.WriteSection("trace", "session_id: session-123"); err != nil {
		t.Fatalf("WriteSection(trace) error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read verification log: %v", err)
	}
	for _, text := range []string{"=== run ===", "scope: fixture.go", "=== trace ===", "session_id: session-123"} {
		if !strings.Contains(string(contents), text) {
			t.Fatalf("verification log does not contain %q:\n%s", text, contents)
		}
	}
}
