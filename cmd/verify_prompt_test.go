package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPromptVerifyOptionsDefaults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		answer string
		agent  string
	}{
		{answer: "1", agent: "claude"},
		{answer: "2", agent: "codex"},
		{answer: " Claude ", agent: "claude"},
		{answer: "CODEX", agent: "codex"},
	} {
		t.Run(test.answer, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			options, err := promptVerifyOptions(context.Background(), strings.NewReader(test.answer+"\n\n\n\n\n\n"), &output, verifyFlags{
				jobs: defaultJobs, timeout: defaultTimeout,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if options.agent != test.agent || options.model != "" || options.effort != "" ||
				options.jobs != defaultJobs || options.timeout != defaultTimeout || options.jsonOutput {
				t.Fatalf("unexpected defaults: %+v", options)
			}
		})
	}
}

func TestPromptVerifyOptionsOverridesAndRetries(t *testing.T) {
	t.Parallel()
	// Invalid answers must repeat the same question without losing later choices.
	input := strings.Join([]string{
		"", "unsupported", "2",
		" custom-model ", " custom-effort ",
		"0", "-2", "1.5", "workers", "6",
		"0s", "-1m", "forever", "15m",
		"yaml", "JSON", "",
	}, "\n")
	var output bytes.Buffer
	options, err := promptVerifyOptions(context.Background(), strings.NewReader(input), &output, verifyFlags{
		jobs: defaultJobs, timeout: defaultTimeout,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.agent != "codex" || options.model != "custom-model" || options.effort != "custom-effort" ||
		options.jobs != 6 || options.timeout != 15*time.Minute || !options.jsonOutput {
		t.Fatalf("unexpected selected options: %+v", options)
	}
	for _, message := range []string{
		"choose 1 (claude) or 2 (codex)", "enter a positive whole number",
		"enter a positive duration", "choose text or json",
	} {
		if !strings.Contains(output.String(), message) {
			t.Errorf("missing validation message %q in %q", message, output.String())
		}
	}
}

func TestPromptVerifyOptionsEOFAbortsSetup(t *testing.T) {
	t.Parallel()
	// EOF is cancellation, even at an optional prompt or after a partial answer.
	for _, input := range []string{"", "codex\n", "codex\n\n\n\n\n", "codex\n\n\n\n\njson"} {
		_, err := promptVerifyOptions(context.Background(), strings.NewReader(input), io.Discard, verifyFlags{
			jobs: defaultJobs, timeout: defaultTimeout,
		}, nil)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("input %q: error = %v, want EOF", input, err)
		}
	}
}

func TestPromptVerifyOptionsReadAndWriteErrors(t *testing.T) {
	t.Parallel()
	want := errors.New("broken prompt stream")
	for _, test := range []struct {
		name   string
		input  io.Reader
		output io.Writer
	}{
		{name: "read", input: failingPromptReader{want}, output: io.Discard},
		{name: "write", input: strings.NewReader("codex\n"), output: failingPromptWriter{want}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := promptVerifyOptions(context.Background(), test.input, test.output, verifyFlags{}, nil)
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func TestPromptVerifyOptionsCanceledBeforeInput(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	_, err := promptVerifyOptions(ctx, nil, &output, verifyFlags{}, nil)
	if !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("canceled setup: error = %v, output = %q", err, output.String())
	}
}

func TestReadVerifyAnswerCancelsWhileWaitingForInput(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := readVerifyAnswer(ctx, bufio.NewReader(signalingPromptReader{reader, started}))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("setup did not start reading input")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("setup did not stop promptly after cancellation")
	}
}

func TestVerifyNonTerminalInputDoesNotPrompt(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"verify"},
		{"verify", "."},
		{"verify", "--json"},
		{"verify", "--jobs", "4"},
		{"verify", "--interactive=false"},
	} {
		command, state := newRootCommand("test")
		command.SetArgs(args)
		// This would select an agent if the command accidentally read piped input.
		command.SetIn(strings.NewReader("codex\n\n\n\n\n\n"))
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		code := executeRoot(context.Background(), command, state)
		if code != 2 || !strings.Contains(stderr.String(), "--agent is required") {
			t.Fatalf("%v: exit = %d, stderr = %q", args, code, stderr.String())
		}
		if stdout.Len() != 0 || strings.Contains(stderr.String(), "Select options") || state.verificationLogPath != "" {
			t.Fatalf("%v: noninteractive command started setup or verification", args)
		}
	}
}

func TestVerifyInteractiveFlagPromptsOnlyForMissingOptions(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for _, test := range []struct {
		name    string
		flags   []string
		answers string
		prompts []string
		want    verifyFlags
	}{
		{
			name:    "all prompted with redirected input",
			answers: "codex\n\n\n\n\njson\n",
			prompts: []string{"Harness", "Model override", "Reasoning effort", "Concurrent workers", "Timeout per claim", "Output format"},
			want:    verifyFlags{agent: "codex", jobs: defaultJobs, timeout: defaultTimeout, jsonOutput: true},
		},
		{
			name:    "supplied harness workers and output",
			flags:   []string{"--agent", "codex", "--jobs", "6", "--json", "--append-prompt", "extra context"},
			answers: "custom-model\nhigh\n45s\n",
			prompts: []string{"Model override", "Reasoning effort", "Timeout per claim"},
			want:    verifyFlags{agent: "codex", model: "custom-model", effort: "high", jobs: 6, timeout: 45 * time.Second, jsonOutput: true, appendPrompt: []string{"extra context"}},
		},
		{
			name:    "supplied model effort and timeout",
			flags:   []string{"--model", "custom-model", "--effort", "high", "--timeout", "2m"},
			answers: "claude\n8\njson\n",
			prompts: []string{"Harness", "Concurrent workers", "Output format"},
			want:    verifyFlags{agent: "claude", model: "custom-model", effort: "high", jobs: 8, timeout: 2 * time.Minute, jsonOutput: true},
		},
		{
			name:    "explicit empty and false values are skipped",
			flags:   []string{"--model=", "--effort=", "--jobs", "4", "--timeout", "10m", "--json=false"},
			answers: "codex\n",
			prompts: []string{"Harness"},
			want:    verifyFlags{agent: "codex", jobs: defaultJobs, timeout: defaultTimeout},
		},
		{
			name:  "all supplied without reading input",
			flags: []string{"--agent", "claude", "--model", "custom-model", "--effort", "high", "--jobs", "2", "--timeout", "30s", "--json"},
			want:  verifyFlags{agent: "claude", model: "custom-model", effort: "high", jobs: 2, timeout: 30 * time.Second, jsonOutput: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			command, state := newRootCommand("test")
			args := append([]string{"verify", "--interactive", repository}, test.flags...)
			command.SetArgs(args)
			command.SetIn(strings.NewReader(test.answers))
			if test.answers == "" {
				command.SetIn(failingPromptReader{errors.New("unexpected read of input")})
			}
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			code := executeRoot(context.Background(), command, state)
			t.Cleanup(func() { _ = os.Remove(state.verificationLogPath) })
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			for _, prompt := range []string{"Harness", "Model override", "Reasoning effort", "Concurrent workers", "Timeout per claim", "Output format"} {
				wantPrompt := false
				for _, expected := range test.prompts {
					wantPrompt = wantPrompt || prompt == expected
				}
				if got := strings.Contains(stderr.String(), prompt); got != wantPrompt {
					t.Errorf("prompt %q present = %v, want %v; stderr = %q", prompt, got, wantPrompt, stderr.String())
				}
			}
			if got := strings.Contains(stderr.String(), "Select options"); got != (len(test.prompts) > 0) {
				t.Errorf("setup heading present = %v; stderr = %q", got, stderr.String())
			}
			if json.Valid(stdout.Bytes()) != test.want.jsonOutput {
				t.Fatalf("output does not match selected format: %s", stdout.String())
			}
			if test.want.jsonOutput {
				var document struct {
					Agent           struct{ Name, Model, Effort string }
					AppendedContext bool `json:"appended_context"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
					t.Fatal(err)
				}
				if document.Agent.Name != test.want.agent || document.Agent.Model != test.want.model || document.Agent.Effort != test.want.effort ||
					document.AppendedContext != (len(test.want.appendPrompt) > 0) {
					t.Fatalf("unexpected report metadata: %+v", document)
				}
			}
			log, err := os.ReadFile(state.verificationLogPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range []string{
				"agent: " + test.want.agent, "model: " + test.want.model, "effort: " + test.want.effort,
				fmt.Sprintf("jobs: %d", test.want.jobs), "per_claim_timeout: " + test.want.timeout.String(),
			} {
				if !strings.Contains(string(log), "\n"+line+"\n") {
					t.Errorf("verification log is missing selected option %q", line)
				}
			}
		})
	}
}

func TestVerifyInteractiveDoesNotReplaceInvalidExplicitOptions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		flag, value, message string
	}{
		{"--agent", "", "--agent is required"},
		{"--jobs", "0", "--jobs must be positive"},
		{"--timeout", "0s", "--timeout must be positive"},
	} {
		command, state := newRootCommand("test")
		command.SetArgs([]string{
			"verify", "--interactive", "--agent", "codex", "--model=", "--effort=",
			"--jobs", "4", "--timeout", "10m", "--json=false", test.flag, test.value,
		})
		command.SetIn(failingPromptReader{errors.New("unexpected read of input")})
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		code := executeRoot(context.Background(), command, state)
		if code != 2 || !strings.Contains(stderr.String(), test.message) {
			t.Fatalf("%s: exit code = %d, stderr = %q", test.flag, code, stderr.String())
		}
		if strings.Contains(stderr.String(), "Select options") || stdout.Len() != 0 || state.verificationLogPath != "" {
			t.Fatalf("%s: invalid explicit option started prompting or verification", test.flag)
		}
	}
}

func TestVerifyInteractiveEOFDoesNotStartVerification(t *testing.T) {
	t.Parallel()
	command, state := newRootCommand("test")
	command.SetArgs([]string{"verify", "--interactive", "--agent", "codex"})
	command.SetIn(strings.NewReader(""))
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	code := executeRoot(context.Background(), command, state)
	if code != 2 || !strings.Contains(stderr.String(), "EOF") || stdout.Len() != 0 || state.verificationLogPath != "" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q, log = %q", code, stdout.String(), stderr.String(), state.verificationLogPath)
	}
}

func TestIsTerminalInputRejectsPipe(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if isTerminalInput(reader) {
		t.Fatal("pipe was treated as a terminal")
	}
}

type failingPromptReader struct{ err error }

func (reader failingPromptReader) Read([]byte) (int, error) { return 0, reader.err }

type failingPromptWriter struct{ err error }

func (writer failingPromptWriter) Write([]byte) (int, error) { return 0, writer.err }

type signalingPromptReader struct {
	io.Reader
	started chan struct{}
}

func (reader signalingPromptReader) Read(contents []byte) (int, error) {
	close(reader.started)
	return reader.Reader.Read(contents)
}
