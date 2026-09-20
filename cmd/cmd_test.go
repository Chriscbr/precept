package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionCommands(t *testing.T) {
	t.Parallel()

	stdout, _, err := executeCommand(t, []string{"version"})
	if err != nil {
		t.Fatalf("precept version: %v", err)
	}
	if stdout != "0.1.0\n" {
		t.Fatalf("precept version output = %q, want %q", stdout, "0.1.0\n")
	}

	stdout, _, err = executeCommand(t, []string{"--version"})
	if err != nil {
		t.Fatalf("precept --version: %v", err)
	}
	if !strings.Contains(stdout, "0.1.0") {
		t.Fatalf("precept --version output = %q, want version", stdout)
	}
}

func TestCommandsDefaultToCurrentDirectory(t *testing.T) {
	repository := t.TempDir()
	git := exec.Command("git", "init", "-q", repository)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatalf("change to fixture repository: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore current directory: %v", err)
		}
	})

	stdout, _, err := executeCommand(t, []string{"list", "--json"})
	if err != nil {
		t.Fatalf("precept list without scope: %v", err)
	}
	var listResult listDocument
	if err := json.Unmarshal([]byte(stdout), &listResult); err != nil {
		t.Fatalf("decode list JSON: %v\nstdout:\n%s", err, stdout)
	}
	if listResult.Scope != "." {
		t.Fatalf("list scope = %q, want current directory", listResult.Scope)
	}

	binDirectory := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDirectory, 0o755); err != nil {
		t.Fatalf("create fake bin directory: %v", err)
	}
	const fakeCodex = `#!/bin/sh
exit 9
`
	mustWriteFile(t, filepath.Join(binDirectory, "codex"), fakeCodex, 0o755)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, exitCode, logPath := executeCLI(t, []string{"verify", "--harness", "codex", "--json"})
	if exitCode != 0 {
		t.Fatalf("precept verify without scope exit code = %d", exitCode)
	}
	var verifyResult struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(stdout), &verifyResult); err != nil {
		t.Fatalf("decode verify JSON: %v\nstdout:\n%s", err, stdout)
	}
	if verifyResult.Scope != "." {
		t.Fatalf("verify scope = %q, want current directory", verifyResult.Scope)
	}
	if strings.Contains(stdout, `"log_path"`) {
		t.Fatalf("verify JSON duplicated the final log-path announcement:\n%s", stdout)
	}
	if !strings.Contains(stderr, "No claims found in .") {
		t.Fatalf("verify stderr does not explain the empty scope:\n%s", stderr)
	}
	assertFinalLogLine(t, stderr, logPath)
	t.Cleanup(func() { _ = os.Remove(logPath) })
}

func TestVerificationLogPathIsLastAfterOperationalError(t *testing.T) {
	binDirectory := t.TempDir()
	mustWriteFile(t, filepath.Join(binDirectory, "codex"), "#!/bin/sh\nexit 9\n", 0o755)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	missingContext := filepath.Join(t.TempDir(), "missing-context.md")
	command, state := newRootCommand("0.1.0")
	command.SetArgs([]string{
		"verify",
		"--harness", "codex",
		"--append-prompt-file", missingContext,
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	if exitCode := executeRoot(context.Background(), command, state); exitCode != 2 {
		t.Fatalf("precept verify exit code = %d, want 2", exitCode)
	}
	if state.verificationLogPath == "" {
		t.Fatal("verify did not record its log path")
	}
	t.Cleanup(func() { _ = os.Remove(state.verificationLogPath) })
	if !strings.Contains(stderr.String(), "error: read appended prompt file") {
		t.Fatalf("verify stderr does not contain the operational error:\n%s", stderr.String())
	}
	assertFinalLogLine(t, stderr.String(), state.verificationLogPath)
}

func TestVerifyMissingAgentFailsBeforeScanningOrLoadingContext(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			stdout, stderr, exitCode, logPath := executeCLI(t, []string{
				"verify", "--harness", agent, "--json",
				"--append-prompt-file", filepath.Join(t.TempDir(), "missing-context.md"),
				filepath.Join(t.TempDir(), "missing-scope"),
			})
			t.Cleanup(func() { _ = os.Remove(logPath) })
			if exitCode != 2 || stdout != "" || !strings.Contains(stderr, "executable \""+agent+"\" was not found in PATH") {
				t.Fatalf("missing agent: exit=%d stdout=%q stderr=%q", exitCode, stdout, stderr)
			}
			for _, unwanted := range []string{"Scanning", "Checking", "read appended prompt file", "missing-scope"} {
				if strings.Contains(stderr, unwanted) {
					t.Errorf("missing agent reached another phase %q: %s", unwanted, stderr)
				}
			}
			assertFinalLogLine(t, stderr, logPath)
		})
	}
}

func TestVerifyAgentCrashProducesPerClaimErrors(t *testing.T) {
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	mustWriteFile(t, filepath.Join(repository, "fixture.go"), `package fixture

// POSTCONDITION: the result is one
func One() int { return 1 }

// POSTCONDITION: the result is two
func Two() int { return 2 }
`, 0o644)
	binDirectory := t.TempDir()
	callLog := filepath.Join(t.TempDir(), "calls")
	mustWriteFile(t, filepath.Join(binDirectory, "codex"), `#!/bin/sh
printf '%s\n' "$1" >> "$PRECEPT_FAKE_CALL_LOG"
printf '%s\n' 'agent crashed' >&2
kill -TERM $$
`, 0o755)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PRECEPT_FAKE_CALL_LOG", callLog)

	for _, jsonOutput := range []bool{false, true} {
		arguments := []string{"verify", "--harness", "codex", "--jobs", "2", repository}
		if jsonOutput {
			arguments = append(arguments, "--json")
		}
		stdout, stderr, exitCode, logPath := executeCLI(t, arguments)
		t.Cleanup(func() { _ = os.Remove(logPath) })
		if exitCode != 2 {
			t.Fatalf("crashed agent exit=%d stdout=%s stderr=%s", exitCode, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, "Checking") || strings.Contains(stdout+stderr, "\x1b") {
			t.Fatalf("plain output contains a checking phase or ANSI controls: %q %q", stdout, stderr)
		}
		if !strings.Contains(stderr, "Scanning for claims in "+repository) || !strings.Contains(stderr, "Found 2 claims in 1 file") {
			t.Fatalf("scan lifecycle was not retained: %s", stderr)
		}
		if jsonOutput {
			var document struct {
				Outcomes []struct {
					Error string `json:"error"`
				} `json:"outcomes"`
				Summary struct{ Total, Errors int } `json:"summary"`
			}
			if err := json.Unmarshal([]byte(stdout), &document); err != nil {
				t.Fatalf("JSON stdout is not one clean document: %v\n%s", err, stdout)
			}
			if len(document.Outcomes) != 2 || document.Summary.Total != 2 || document.Summary.Errors != 2 {
				t.Fatalf("crash JSON summary = %+v", document)
			}
			for _, outcome := range document.Outcomes {
				if !strings.Contains(outcome.Error, "agent crashed") {
					t.Errorf("claim omitted crash detail: %+v", outcome)
				}
			}
		} else {
			for _, want := range []string{"fixture.One [POSTCONDITION] (one-postcondition-1)  ! ERROR", "fixture.Two [POSTCONDITION] (two-postcondition-1)  ! ERROR", "2 claims: 0 holds, 0 violated, 0 inconclusive, 2 errors"} {
				if strings.Count(stdout, want) != 1 {
					t.Errorf("want one %q in streamed report:\n%s", want, stdout)
				}
			}
			if strings.Count(stdout, "agent crashed") != 2 || strings.Contains(stdout, "Scanning") {
				t.Errorf("results and progress were not separated: %s", stdout)
			}
		}
		assertFinalLogLine(t, stderr, logPath)
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(calls); len(strings.Fields(got)) != 4 || strings.Contains(got, "--version") {
		t.Fatalf("agent calls = %q; expected four claim runs and no version probe", got)
	}
}

func TestListOutput(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	git := exec.Command("git", "init", "-q", repository)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	fixturePath := filepath.Join(repository, "fixture.go")
	mustWriteFile(t, fixturePath, `package fixture

// INVARIANT: always returns one
func Example() int { return 1 }
`, 0o644)
	mustWriteFile(t, filepath.Join(repository, "other.go"), `package fixture

// POSTCONDITION: this file was not selected
func Other() {}
`, 0o644)

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "text",
			args: []string{"list", fixturePath},
			want: "fixture.Example [INVARIANT] (example-invariant-1)\n  Source  fixture.go:3\n  Claim   always returns one\n\n1 claim in 1 file\n",
		},
		{
			name: "compact",
			args: []string{"list", "--compact", fixturePath},
			want: "ID                   KIND       SYMBOL           SOURCE\nexample-invariant-1  INVARIANT  fixture.Example  fixture.go:3\n\n1 claim in 1 file\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := executeCommand(t, test.args)
			if err != nil {
				t.Fatalf("precept list: %v", err)
			}
			if stdout != test.want {
				t.Fatalf("list output =\n%s\nwant:\n%s", stdout, test.want)
			}
			if !strings.Contains(stderr, "Scanning for claims in "+fixturePath) {
				t.Fatalf("list stderr does not immediately identify its scan scope:\n%s", stderr)
			}
		})
	}
	t.Run("json ignores compact", func(t *testing.T) {
		stdout, stderr, err := executeCommand(t, []string{"list", "--json", fixturePath})
		if err != nil {
			t.Fatalf("precept list --json: %v", err)
		}
		compactStdout, compactStderr, err := executeCommand(t, []string{"list", "--json", "--compact", fixturePath})
		if err != nil {
			t.Fatalf("precept list --json --compact: %v", err)
		}
		if !json.Valid([]byte(compactStdout)) || compactStdout != stdout || compactStderr != stderr {
			t.Fatalf("--compact changed JSON output:\nstdout:\n%s\nstderr:\n%s", compactStdout, compactStderr)
		}
	})
}

func TestListStopsPromptlyWhenCanceledAfterScanningStarts(t *testing.T) {
	repository := t.TempDir()
	git := exec.Command("git", "init", "-q", repository)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	mustWriteFile(t, filepath.Join(repository, "fixture.go"), "package fixture\n", 0o644)

	command := NewRootCommand("0.1.0")
	command.SetArgs([]string{"list", repository})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetOut(&stdout)
	command.SetErr(cancelingWriter{writer: &stderr, cancel: cancel})
	startedAt := time.Now()
	err := command.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("precept list error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("precept list took %s to stop after cancellation", elapsed)
	}
	if !strings.Contains(stderr.String(), "Scanning for claims in "+repository) {
		t.Fatalf("list stderr does not identify its scan scope:\n%s", stderr.String())
	}
}

func TestResolveScopeCancelsRunningGit(t *testing.T) {
	repository := t.TempDir()
	binDirectory := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	startedPath := filepath.Join(t.TempDir(), "git-started")
	const fakeGit = `#!/bin/sh
: > "$PRECEPT_FAKE_GIT_STARTED"
kill -STOP $$
`
	mustWriteFile(t, filepath.Join(binDirectory, "git"), fakeGit, 0o755)
	t.Setenv("PRECEPT_FAKE_GIT_STARTED", startedPath)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := resolveScope(ctx, repository)
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake git did not start within one second")
		}
		time.Sleep(5 * time.Millisecond)
	}
	startedAt := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolveScope() error = %v, want context cancellation", err)
		}
		if elapsed := time.Since(startedAt); elapsed > time.Second {
			t.Fatalf("resolveScope() took %s to stop after cancellation", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("resolveScope() did not stop within one second of cancellation")
	}
}

func TestUnsupportedFileExplainsCurrentSourceSupport(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	git := exec.Command("git", "init", "-q", repository)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	unsupportedPath := filepath.Join(repository, "fixture.txt")
	mustWriteFile(t, unsupportedPath, "// INVARIANT: ignored\n", 0o644)

	_, stderr, err := executeCommand(t, []string{"list", unsupportedPath})
	if err == nil {
		t.Fatal("precept list accepted an unsupported file")
	}
	if !strings.Contains(err.Error(), "expected a .go file") {
		t.Fatalf("error = %q, want current source support to be explicit", err)
	}
	if !strings.Contains(stderr, "Scanning for claims in "+unsupportedPath) {
		t.Fatalf("list stderr does not identify its scan scope:\n%s", stderr)
	}
}

func TestCommandsUseJSONFlag(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"list", "verify"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, _, err := executeCommand(t, []string{name, "--help"})
			if err != nil {
				t.Fatalf("precept %s --help: %v", name, err)
			}
			if !strings.Contains(stdout, "--json") {
				t.Fatalf("help does not describe --json:\n%s", stdout)
			}
			if strings.Contains(stdout, "--format") {
				t.Fatalf("help still describes removed --format flag:\n%s", stdout)
			}
			if !strings.Contains(stdout, "[file-or-directory]") {
				t.Fatalf("help does not use the generic file-or-directory argument:\n%s", stdout)
			}
		})
	}
}

func TestCommandsExplainFileOrDirectoryArgument(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"list", "verify"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := executeCommand(t, []string{name, "first", "second"})
			if err == nil {
				t.Fatal("command with two file-or-directory arguments succeeded")
			}
			for _, text := range []string{"at most one file-or-directory argument", "current directory"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("error = %q, want it to contain %q", err, text)
				}
			}
		})
	}
}

func TestValidateVerifyFlags(t *testing.T) {
	t.Parallel()

	valid := verifyFlags{harness: "codex", jobs: 1, timeout: time.Second}
	if err := validateVerifyFlags(valid); err != nil {
		t.Fatalf("validateVerifyFlags(valid) = %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*verifyFlags)
		message string
	}{
		{name: "missing harness", mutate: func(flags *verifyFlags) { flags.harness = "" }, message: "--harness"},
		{name: "zero jobs", mutate: func(flags *verifyFlags) { flags.jobs = 0 }, message: "--jobs"},
		{name: "empty claim ID", mutate: func(flags *verifyFlags) { flags.claims = []string{""} }, message: "--claim"},
		{name: "blank claim ID", mutate: func(flags *verifyFlags) { flags.claims = []string{" \t"} }, message: "--claim"},
		{name: "zero timeout", mutate: func(flags *verifyFlags) { flags.timeout = 0 }, message: "--timeout"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			flags := valid
			test.mutate(&flags)
			err := validateVerifyFlags(flags)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validateVerifyFlags() = %v, want error containing %q", err, test.message)
			}
		})
	}
}

func TestLoadAppendedSections(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "context.md")
	mustWriteFile(t, filename, "context from file\n", 0o600)
	sections, err := loadAppendedSections(
		[]string{"first", "  ", "second"},
		[]string{filename},
	)
	if err != nil {
		t.Fatalf("loadAppendedSections() error = %v", err)
	}
	want := []string{"first", "second", "context from file\n"}
	if strings.Join(sections, "|") != strings.Join(want, "|") {
		t.Fatalf("loadAppendedSections() = %#v, want %#v", sections, want)
	}
}

func TestVerifyWithFakeCodexIsStatelessAndRunsOncePerClaim(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	git := exec.Command("git", "init", "-q", repository)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	const source = `package fixture

// PRECONDITION: One always returns one.
func One() int { return 1 }

func Two() int {
	// ASSERTION: Two is about to return two.
	// This continuation is part of the second claim.
	return 2
}
`
	sourcePath := filepath.Join(repository, "fixture.go")
	mustWriteFile(t, sourcePath, source, 0o644)

	binDirectory := filepath.Join(base, "bin")
	if err := os.MkdirAll(binDirectory, 0o755); err != nil {
		t.Fatalf("create fake bin directory: %v", err)
	}
	callLog := filepath.Join(base, "calls.log")
	const fakeCodex = `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'codex-cli fake-1.0'
  exit 0
fi
input=$(cat)
case "$input" in
  *"shared verifier context"*) ;;
  *) printf '%s\n' 'missing appended context' >&2; exit 9 ;;
esac
printf '%s\n' run >> "$PRECEPT_FAKE_CALL_LOG"
printf '%s\n' '{"type":"thread.started","thread_id":"fake-session"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"holds\",\"summary\":\"the fixture implementation is direct\",\"evidence\":[{\"file\":\"fixture.go\",\"start_line\":4,\"end_line\":4,\"reason\":\"the return is constant\"}],\"counterexample\":\"\"}"}}'
`
	mustWriteFile(t, filepath.Join(binDirectory, "codex"), fakeCodex, 0o755)
	codexHome := filepath.Join(base, "codex-home")
	now := time.Now()
	conversationDirectory := filepath.Join(
		codexHome,
		"sessions",
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"),
	)
	if err := os.MkdirAll(conversationDirectory, 0o755); err != nil {
		t.Fatalf("create fake Codex session directory: %v", err)
	}
	conversationPath := filepath.Join(conversationDirectory, "rollout-2026-07-21T00-00-00-fake-session.jsonl")
	mustWriteFile(t, conversationPath, "fake conversation\n", 0o600)
	t.Setenv("PRECEPT_FAKE_CALL_LOG", callLog)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, exitCode, logPath := executeCLI(t, []string{
		"verify",
		"--harness", "codex",
		"--jobs", "2",
		"--json",
		"--append-prompt", "shared verifier context",
		sourcePath,
	})
	if exitCode != 0 {
		t.Fatalf("precept verify exit code = %d\nstderr:\n%s", exitCode, stderr)
	}

	var document struct {
		Agent struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agent"`
		Summary struct {
			Total int `json:"total"`
			Holds int `json:"holds"`
		} `json:"summary"`
		Outcomes []struct {
			SessionID     string `json:"session_id"`
			ResumeCommand string `json:"resume_command"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode verify JSON: %v\nstdout:\n%s", err, stdout)
	}
	if document.Agent.Name != "codex" || document.Agent.Version != "" {
		t.Fatalf("agent metadata = %#v", document.Agent)
	}
	if document.Summary.Total != 2 || document.Summary.Holds != 2 {
		t.Fatalf("summary = %#v, want two holding claims", document.Summary)
	}
	if strings.Contains(stdout, `"log_path"`) {
		t.Fatalf("verify JSON duplicated the final log-path announcement:\n%s", stdout)
	}
	assertFinalLogLine(t, stderr, logPath)
	t.Cleanup(func() { _ = os.Remove(logPath) })
	if len(document.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v, want two", document.Outcomes)
	}
	for _, outcome := range document.Outcomes {
		if outcome.SessionID != "fake-session" || outcome.ResumeCommand != "codex resume fake-session" {
			t.Fatalf("session metadata = %#v", outcome)
		}
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read fake-agent call log: %v", err)
	}
	if got := len(strings.Fields(string(calls))); got != 2 {
		t.Fatalf("fake agent calls = %d, want 2; log = %q", got, calls)
	}
	gotSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source after verification: %v", err)
	}
	if string(gotSource) != source {
		t.Fatal("verification modified the source containing claims")
	}
	logContents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read verification log: %v", err)
	}
	for _, text := range []string{"=== run ===", "harness: codex", "=== discovery ===", "session_id: fake-session", "codex resume fake-session", "conversation_path: " + conversationPath, "=== normalized report ==="} {
		if !strings.Contains(string(logContents), text) {
			t.Errorf("verification log does not contain %q", text)
		}
	}
	for _, unwanted := range []string{"shared verifier context", "rendered prompt", "agent stdout", "agent stderr"} {
		if strings.Contains(string(logContents), unwanted) {
			t.Errorf("verification log unexpectedly contains %q", unwanted)
		}
	}
	entries, err := os.ReadDir(repository)
	if err != nil {
		t.Fatalf("read repository after verification: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" && entry.Name() != "fixture.go" {
			t.Errorf("verification created unexpected repository entry %q", entry.Name())
		}
	}
}

func executeCommand(t *testing.T, arguments []string) (string, string, error) {
	t.Helper()
	command := NewRootCommand("0.1.0")
	command.SetArgs(arguments)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func executeCLI(t *testing.T, arguments []string) (string, string, int, string) {
	t.Helper()
	command, state := newRootCommand("0.1.0")
	command.SetArgs(arguments)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	exitCode := executeRoot(context.Background(), command, state)
	return stdout.String(), stderr.String(), exitCode, state.verificationLogPath
}

func assertFinalLogLine(t *testing.T, stderr, logPath string) {
	t.Helper()
	if logPath == "" {
		t.Fatal("log path is empty")
	}
	line := "Log: " + logPath + "\n"
	if count := strings.Count(stderr, line); count != 1 {
		t.Fatalf("log line count = %d, want 1; stderr:\n%s", count, stderr)
	}
	if !strings.HasSuffix(stderr, line) {
		t.Fatalf("log is not the final output line:\n%s", stderr)
	}
}

func mustWriteFile(t *testing.T, filename, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filename, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

type cancelingWriter struct {
	writer *bytes.Buffer
	cancel context.CancelFunc
}

func (writer cancelingWriter) Write(contents []byte) (int, error) {
	written, err := writer.writer.Write(contents)
	writer.cancel()
	return written, err
}
