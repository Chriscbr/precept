package report

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"precept/internal/discover"
	"precept/internal/verify"
)

func fixtureRun() Run {
	startedAt := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	return Run{
		PreceptVersion: "0.1.0",
		Scope:          "example",
		Agent: Agent{
			Name:    "codex",
			Version: "codex-cli 1.2.3",
			Model:   "gpt-5-mini",
			Effort:  "high",
		},
		StartedAt:       startedAt,
		FinishedAt:      startedAt.Add(1500 * time.Millisecond),
		AppendedContext: true,
		Diagnostics: []discover.Diagnostic{{
			File:    "example/free.go",
			Line:    7,
			Message: "INVARIANT claim is empty",
		}},
		Outcomes: []verify.Outcome{
			{
				Claim: discover.Claim{
					Marker:     discover.MarkerPrecondition,
					Text:       "result is within [lo, hi]",
					Package:    "example",
					Kind:       "func",
					Symbol:     "Clamp",
					File:       "example/clamp.go",
					MarkerLine: 11,
					StartLine:  12,
					EndLine:    14,
				},
				Result: &verify.Result{
					Verdict: verify.VerdictHolds,
					Summary: "both bounds are applied",
					Evidence: []verify.Evidence{{
						File:      "example/clamp.go",
						StartLine: 12,
						EndLine:   14,
						Reason:    "min and max apply both bounds",
					}},
				},
				Duration:  10 * time.Millisecond,
				SessionID: "codex-holds-123",
			},
			{
				Claim: discover.Claim{
					Marker:     discover.MarkerInvariant,
					Text:       "missing keys are not reported as hits",
					Package:    "example",
					Kind:       "method",
					Symbol:     "(*Cache).Get",
					File:       "example/cache.go",
					MarkerLine: 42,
					StartLine:  43,
					EndLine:    49,
				},
				Result: &verify.Result{
					Verdict: verify.VerdictViolated,
					Summary: "the zero value is reported as a hit",
					Evidence: []verify.Evidence{{
						File:      "example/cache.go",
						StartLine: 46,
						EndLine:   46,
						Reason:    "the method always returns true",
					}},
					Counterexample: "calling Get with an absent key returns (_, true)",
				},
				Duration:  20 * time.Millisecond,
				SessionID: "codex-violated-456",
			},
			{
				Claim: discover.Claim{
					Marker:     discover.MarkerPostcondition,
					Text:       "configuration is valid",
					Package:    "example",
					Kind:       "type",
					Symbol:     "Config",
					File:       "example/config.go",
					MarkerLine: 19,
					StartLine:  20,
					EndLine:    25,
				},
				Result: &verify.Result{
					Verdict: verify.VerdictInconclusive,
					Summary: "validity depends on values loaded at runtime",
					Evidence: []verify.Evidence{{
						File:      "example/config.go",
						StartLine: 22,
						EndLine:   24,
						Reason:    "runtime values are loaded from the environment",
					}},
				},
				Duration: 30 * time.Millisecond,
			},
			{
				Claim: discover.Claim{
					Marker:     discover.MarkerInvariant,
					Text:       "parsing never panics",
					Package:    "example",
					Kind:       "func",
					Symbol:     "Parse",
					File:       "example/parse.go",
					MarkerLine: 31,
					StartLine:  32,
					EndLine:    38,
				},
				Error:     errors.New("validation timed out after 10m0s"),
				Duration:  40 * time.Millisecond,
				SessionID: "codex-error-789",
			},
		},
	}
}

func TestWriteTextGolden(t *testing.T) {
	t.Parallel()
	assertGolden(t, "text.golden", func(buffer *bytes.Buffer) error {
		return WriteText(buffer, fixtureRun())
	})
}

func TestWriteTextTerminalStyle(t *testing.T) {
	t.Parallel()

	var styled bytes.Buffer
	if err := writeText(&styled, fixtureRun(), textStyle{enabled: true}); err != nil {
		t.Fatalf("write styled text: %v", err)
	}
	for _, fragment := range []string{
		"\x1b[32m✓\x1b[0m",
		"\x1b[31m✗\x1b[0m",
		"\x1b[33m?\x1b[0m",
		"\x1b[31m!\x1b[0m",
		"\x1b[1mexample.Clamp\x1b[0m",
		"[PRECONDITION]",
		"\x1b[1m[outcome]:\x1b[0m",
		"\x1b[1m[reason]:\x1b[0m",
		"\x1b[1m[supporting evidence]:\x1b[0m",
		"\x1b[1m[counterexample]:\x1b[0m",
		"\x1b[1m[agent session]:\x1b[0m",
		"\x1b[32mHolds\x1b[0m",
		"\x1b[31mViolated\x1b[0m",
		"\x1b[33mInconclusive\x1b[0m",
		"\x1b[31mError\x1b[0m",
		"\x1b[90m(example/clamp.go:12-14)\x1b[0m",
	} {
		if !strings.Contains(styled.String(), fragment) {
			t.Errorf("styled output does not contain %q:\n%s", fragment, styled.String())
		}
	}

	var plain bytes.Buffer
	if err := writeText(&plain, fixtureRun(), textStyle{}); err != nil {
		t.Fatalf("write plain text: %v", err)
	}
	if got := stripReportANSI(styled.String()); got != plain.String() {
		t.Fatalf("styled structure differs after removing ANSI\ngot:\n%s\nwant:\n%s", got, plain.String())
	}
}

func TestWriteTextEscapesTerminalControls(t *testing.T) {
	t.Parallel()

	run := fixtureRun()
	run.Outcomes = run.Outcomes[:1]
	run.Outcomes[0].Result.Summary = "safe\x1b]52;clipboard\a\ncontinued"
	run.Outcomes[0].Result.Evidence[0].Reason = "evidence\rspoof"

	var output bytes.Buffer
	if err := writeText(&output, run, textStyle{enabled: true}); err != nil {
		t.Fatalf("write styled text: %v", err)
	}
	if strings.Contains(output.String(), "\x1b]52") || strings.Contains(output.String(), "\a") {
		t.Fatalf("styled output contains an untrusted terminal control:\n%s", output.String())
	}
	for _, want := range []string{`safe\u{1b}]52;clipboard\u{7}`, "continued", `evidence\rspoof`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("styled output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestWriteTextRendersClaimTypeErrorWithEvidence(t *testing.T) {
	t.Parallel()

	run := Run{Outcomes: []verify.Outcome{{
		Claim: discover.Claim{
			Marker:     discover.MarkerPrecondition,
			Package:    "example",
			Kind:       "func",
			Symbol:     "Build",
			File:       "example/build.go",
			MarkerLine: 3,
		},
		Result: &verify.Result{
			Verdict: verify.VerdictError,
			Summary: "the claim only describes a return value",
			Evidence: []verify.Evidence{{
				File:      "example/build.go",
				StartLine: 3,
				EndLine:   4,
				Reason:    "the claim is marked as an entry condition",
			}},
		},
	}}}

	var output bytes.Buffer
	if err := writeText(&output, run, textStyle{}); err != nil {
		t.Fatalf("writeText() error = %v", err)
	}
	for _, fragment := range []string{
		"! example.Build [PRECONDITION]",
		"[outcome]: Error",
		"[reason]: the claim only describes a return value",
		"[supporting evidence]:",
		"1 claims: 0 holds, 0 violated, 0 inconclusive, 1 error",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("text output does not contain %q:\n%s", fragment, output.String())
		}
	}
}

func TestColorEnvironmentAllows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		env    map[string]string
		allows bool
	}{
		{name: "default", allows: true},
		{name: "terminal", env: map[string]string{"TERM": "xterm-256color"}, allows: true},
		{name: "no color", env: map[string]string{"NO_COLOR": "1"}},
		{name: "empty no color", env: map[string]string{"NO_COLOR": ""}},
		{name: "dumb terminal", env: map[string]string{"TERM": "dumb"}},
		{name: "case insensitive dumb terminal", env: map[string]string{"TERM": " DUMB "}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lookup := func(key string) (string, bool) {
				value, ok := test.env[key]
				return value, ok
			}
			if got := colorEnvironmentAllows(lookup); got != test.allows {
				t.Fatalf("colorEnvironmentAllows() = %t, want %t", got, test.allows)
			}
		})
	}

	lookup := func(string) (string, bool) { return "", false }
	if shouldStyle(&bytes.Buffer{}, lookup) {
		t.Fatal("shouldStyle() enabled ANSI for a redirected buffer")
	}
}

func TestWriteJSONGolden(t *testing.T) {
	t.Parallel()
	assertGolden(t, "json.golden", func(buffer *bytes.Buffer) error {
		return WriteJSON(buffer, fixtureRun())
	})
}

func TestWriteDispatchesFormats(t *testing.T) {
	t.Parallel()
	for _, format := range []Format{FormatText, FormatJSON} {
		var buffer bytes.Buffer
		if err := Write(&buffer, format, Run{}); err != nil {
			t.Errorf("Write(%q) error = %v", format, err)
		}
		if buffer.Len() == 0 {
			t.Errorf("Write(%q) produced no output", format)
		}
	}
	if err := Write(&bytes.Buffer{}, "yaml", Run{}); err == nil {
		t.Fatal("Write(\"yaml\") error = nil, want unsupported format error")
	}
}

func TestExitCodePrecedence(t *testing.T) {
	t.Parallel()

	result := func(verdict verify.Verdict) verify.Outcome {
		return verify.Outcome{Result: &verify.Result{
			Verdict: verdict,
			Summary: "summary",
			Evidence: []verify.Evidence{{
				File:      "example/example.go",
				StartLine: 1,
				EndLine:   1,
				Reason:    "test evidence",
			}},
		}}
	}
	tests := []struct {
		name     string
		outcomes []verify.Outcome
		want     int
	}{
		{name: "no claims", want: 0},
		{name: "all holds", outcomes: []verify.Outcome{result(verify.VerdictHolds)}, want: 0},
		{name: "violated", outcomes: []verify.Outcome{result(verify.VerdictHolds), result(verify.VerdictViolated)}, want: 1},
		{name: "inconclusive", outcomes: []verify.Outcome{result(verify.VerdictInconclusive)}, want: 1},
		{name: "claim type error", outcomes: []verify.Outcome{result(verify.VerdictError)}, want: 2},
		{
			name: "error takes precedence",
			outcomes: []verify.Outcome{
				result(verify.VerdictViolated),
				{Error: errors.New("agent failed")},
			},
			want: 2,
		},
		{name: "missing result is error", outcomes: []verify.Outcome{{}}, want: 2},
		{name: "invalid result is error", outcomes: []verify.Outcome{result("unknown")}, want: 2},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ExitCode(test.outcomes); got != test.want {
				t.Fatalf("ExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestJSONUsesArraysAndStringErrors(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	run := Run{Outcomes: []verify.Outcome{{Error: errors.New("agent failed")}}}
	if err := WriteJSON(&buffer, run); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	got := buffer.String()
	for _, fragment := range []string{
		`"diagnostics": []`,
		`"outcomes": [`,
		`"error": "agent failed"`,
	} {
		if !bytes.Contains(buffer.Bytes(), []byte(fragment)) {
			t.Errorf("JSON output does not contain %s:\n%s", fragment, got)
		}
	}
	if bytes.Contains(buffer.Bytes(), []byte(`"log_path"`)) {
		t.Errorf("JSON output contains the process-owned log path:\n%s", got)
	}
	if bytes.Contains(buffer.Bytes(), []byte(`"error": {`)) {
		t.Errorf("JSON output serialized a Go error as an object:\n%s", got)
	}
}

func TestResumeCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentName string
		sessionID string
		want      string
	}{
		{name: "codex", agentName: "codex", sessionID: "thread-123", want: "codex resume thread-123"},
		{name: "claude", agentName: " CLAUDE ", sessionID: "session-456", want: "claude --resume session-456"},
		{name: "quotes unusual ID", agentName: "codex", sessionID: "two words", want: "codex resume 'two words'"},
		{name: "empty session", agentName: "codex"},
		{name: "unsupported agent", agentName: "other", sessionID: "session-789"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ResumeCommand(test.agentName, test.sessionID); got != test.want {
				t.Fatalf("ResumeCommand(%q, %q) = %q, want %q", test.agentName, test.sessionID, got, test.want)
			}
		})
	}
}

func assertGolden(t *testing.T, name string, render func(*bytes.Buffer) error) {
	t.Helper()
	var buffer bytes.Buffer
	if err := render(&buffer); err != nil {
		t.Fatalf("render report: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := buffer.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("report mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func stripReportANSI(value string) string {
	return strings.NewReplacer(
		"\x1b[0m", "",
		"\x1b[1m", "",
		"\x1b[31m", "",
		"\x1b[32m", "",
		"\x1b[33m", "",
		"\x1b[90m", "",
	).Replace(value)
}
