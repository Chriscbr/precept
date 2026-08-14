package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/harness"
	"github.com/Chriscbr/precept/internal/report"
	"github.com/Chriscbr/precept/internal/textsafe"
	"github.com/Chriscbr/precept/internal/verify"
)

type verificationLog struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func newVerificationLog(startedAt time.Time) (*verificationLog, error) {
	stamp := startedAt.UTC().Format("20060102T150405.000000000Z")
	file, err := os.CreateTemp("/tmp", "precept-verify-"+stamp+"-*.log")
	if err != nil {
		return nil, fmt.Errorf("create verification log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("secure verification log %q: %w", file.Name(), err)
	}
	path, err := filepath.Abs(file.Name())
	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("resolve verification log path: %w", err)
	}
	return &verificationLog{file: file, path: path}, nil
}

func (log *verificationLog) Path() string {
	return log.path
}

func (log *verificationLog) WriteSection(title, body string) error {
	log.mu.Lock()
	defer log.mu.Unlock()

	if _, err := fmt.Fprintf(log.file, "=== %s ===\n", title); err != nil {
		return fmt.Errorf("write verification log section %q: %w", title, err)
	}
	if body != "" {
		if _, err := log.file.WriteString(body); err != nil {
			return fmt.Errorf("write verification log section %q: %w", title, err)
		}
		if !strings.HasSuffix(body, "\n") {
			if _, err := log.file.WriteString("\n"); err != nil {
				return fmt.Errorf("terminate verification log section %q: %w", title, err)
			}
		}
	}
	if _, err := log.file.WriteString("\n"); err != nil {
		return fmt.Errorf("separate verification log section %q: %w", title, err)
	}
	return nil
}

func (log *verificationLog) WriteRunStart(version, scopeArgument string, options verifyFlags, startedAt time.Time) error {
	var body strings.Builder
	_, _ = fmt.Fprintf(&body, "precept_version: %s\n", textsafe.SingleLine(version))
	_, _ = fmt.Fprintf(&body, "started_at: %s\n", startedAt.UTC().Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(&body, "scope_argument: %s\n", textsafe.SingleLine(scopeArgument))
	_, _ = fmt.Fprintf(&body, "agent: %s\n", textsafe.SingleLine(options.agent))
	_, _ = fmt.Fprintf(&body, "model: %s\n", textsafe.SingleLine(options.model))
	_, _ = fmt.Fprintf(&body, "effort: %s\n", textsafe.SingleLine(options.effort))
	_, _ = fmt.Fprintf(&body, "jobs: %d\n", options.jobs)
	_, _ = fmt.Fprintf(&body, "per_claim_timeout: %s\n", options.timeout)
	return log.WriteSection("run", body.String())
}

func (log *verificationLog) WriteDiscovery(scope resolvedScope, result discover.Result) error {
	var body strings.Builder
	_, _ = fmt.Fprintf(&body, "repository_root: %s\n", textsafe.SingleLine(scope.repositoryRoot))
	_, _ = fmt.Fprintf(&body, "scope: %s\n", textsafe.SingleLine(scope.relativePath))
	_, _ = fmt.Fprintf(&body, "claims: %d\n", len(result.Claims))
	_, _ = fmt.Fprintf(&body, "diagnostics: %d\n", len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		location := diagnostic.File
		if diagnostic.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, diagnostic.Line)
		}
		_, _ = fmt.Fprintf(&body, "warning: %s: %s\n", textsafe.SingleLine(location), textsafe.SingleLine(diagnostic.Message))
	}
	return log.WriteSection("discovery", body.String())
}

func (log *verificationLog) WriteHarness(info harness.Info) error {
	body := fmt.Sprintf(
		"name: %s\nexecutable: %s\nversion: %s\n",
		textsafe.SingleLine(info.Name),
		textsafe.SingleLine(info.Executable),
		textsafe.SingleLine(info.Version),
	)
	return log.WriteSection("harness", body)
}

func (log *verificationLog) WriteAgentSession(
	agentName string,
	claim discover.Claim,
	run harness.RunResult,
	failed bool,
) error {
	var body strings.Builder
	_, _ = fmt.Fprintf(&body, "session_id: %s\n", textsafe.SingleLine(run.SessionID))
	_, _ = fmt.Fprintf(&body, "resume_command: %s\n", textsafe.SingleLine(report.ResumeCommand(agentName, run.SessionID)))
	if run.ConversationPath != "" {
		_, _ = fmt.Fprintf(&body, "conversation_path: %s\n", textsafe.SingleLine(run.ConversationPath))
	} else if run.SessionID != "" {
		_, _ = fmt.Fprintln(&body, "conversation_path: unavailable")
	}
	if failed {
		_, _ = fmt.Fprintln(&body, "status: error")
	}
	title := fmt.Sprintf(
		"claim %s:%d %s %s",
		textsafe.SingleLine(claim.File),
		claim.MarkerLine,
		textsafe.SingleLine(string(claim.Marker)),
		textsafe.SingleLine(claim.Symbol),
	)
	return log.WriteSection(title, body.String())
}

func (log *verificationLog) WriteReport(run report.Run) error {
	run.Outcomes = append([]verify.Outcome(nil), run.Outcomes...)
	for index := range run.Outcomes {
		if run.Outcomes[index].Error != nil {
			run.Outcomes[index].Error = errors.New("validation failed; see CLI output and the agent conversation when available")
		}
	}
	var body bytes.Buffer
	if err := report.WriteJSON(&body, run); err != nil {
		return fmt.Errorf("render normalized verification log report: %w", err)
	}
	return log.WriteSection("normalized report", body.String())
}

func (log *verificationLog) WriteCommandExit(commandErr error) error {
	if commandErr == nil {
		return log.WriteSection("command exit", "exit_code: 0\n")
	}
	exitCode := 2
	var typed *exitError
	if errors.As(commandErr, &typed) {
		exitCode = typed.code
	}
	body := fmt.Sprintf("exit_code: %d\n", exitCode)
	return log.WriteSection("command exit", body)
}

func (log *verificationLog) Close() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.file.Close(); err != nil {
		return fmt.Errorf("close verification log: %w", err)
	}
	return nil
}
