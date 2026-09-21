package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyReportFormatsAndDestinations(t *testing.T) {
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	mustWriteFile(t, filepath.Join(repository, "fixture.go"), `package fixture

// POSTCONDITION one: the result is one
func One() int { return 1 }
`, 0o644)
	bin := t.TempDir()
	mustWriteFile(t, filepath.Join(bin, "codex"), `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"violated\",\"summary\":\"fixture summary\",\"evidence\":[{\"file\":\"fixture.go\",\"start_line\":4,\"end_line\":4,\"reason\":\"fixture evidence\"}],\"counterexample\":\"fixture counterexample\"}"}}'
`, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, format := range []string{"text", "json", "markdown"} {
		for _, toFile := range []bool{false, true} {
			name := format + "/stdout"
			if toFile {
				name = format + "/file"
			}
			t.Run(name, func(t *testing.T) {
				destination := "-"
				if toFile {
					destination = filepath.Join(t.TempDir(), "report")
					mustWriteFile(t, destination, strings.Repeat("stale contents", 500), 0o644)
				}
				stdout, stderr, code, log := executeCLI(t, []string{"verify", "--harness", "codex", "--format", format, "--output", destination, repository})
				t.Cleanup(func() { _ = os.Remove(log) })
				if code != 1 {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
				}
				body := stdout
				if toFile {
					if stdout != "" {
						t.Fatalf("file output also wrote stdout: %q", stdout)
					}
					contents, err := os.ReadFile(destination)
					if err != nil {
						t.Fatal(err)
					}
					body = string(contents)
				}
				if strings.Contains(body, "stale contents") || strings.Contains(body, "Scanning") || strings.Contains(body, "\x1b") || strings.Contains(body, "Log:") {
					t.Fatalf("report contains stale contents or progress: %s", body)
				}
				switch format {
				case "json":
					var document struct{ Summary struct{ Total, Violated int } }
					if err := json.Unmarshal([]byte(body), &document); err != nil || document.Summary.Total != 1 || document.Summary.Violated != 1 {
						t.Fatalf("JSON report: %v: %s", err, body)
					}
				case "markdown":
					for _, want := range []string{"## Precept verification\n", "1 violated", "<summary>All claims</summary>", "- ❌ Violated - `one` - `fixture.go:3`", "**Counterexample:** fixture counterexample", "**Supporting evidence:**"} {
						if !strings.Contains(body, want) {
							t.Errorf("missing %q:\n%s", want, body)
						}
					}
				default:
					if !strings.Contains(body, "✗ VIOLATED") || !strings.Contains(body, "1 claim: 0 holds, 1 violated") {
						t.Fatalf("incomplete text report: %s", body)
					}
				}
				if !strings.Contains(stderr, "Scanning for claims") {
					t.Fatalf("stderr lost progress: %s", stderr)
				}
				assertFinalLogLine(t, stderr, log)
			})
		}
	}
}

func TestVerifyMarkdownReportsEarlyFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, destination := range []string{"-", filepath.Join(t.TempDir(), "report.md")} {
		stdout, stderr, code, log := executeCLI(t, []string{"verify", "--harness", "codex", "--format", "markdown", "-o", destination})
		t.Cleanup(func() { _ = os.Remove(log) })
		body := stdout
		if destination != "-" {
			if stdout != "" {
				t.Fatalf("file output contaminated stdout: %s", stdout)
			}
			contents, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			body = string(contents)
		}
		if code != 2 || !strings.Contains(body, "**Verification could not complete.**") || !strings.Contains(body, "was not found in PATH") || strings.Contains(body, "No claims found") {
			t.Fatalf("early error report: exit=%d body=%s stderr=%s", code, body, stderr)
		}
		assertFinalLogLine(t, stderr, log)
	}
}

func TestVerifyOutputFlagsRejectInvalidOptionsBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{
		{"--format", "yaml"}, {"--format", ""}, {"--json", "--format", "markdown"}, {"--format", "text", "--json"},
	} {
		path := filepath.Join(t.TempDir(), "report.md")
		mustWriteFile(t, path, "keep", 0o644)
		args := append([]string{"verify", "--harness", "codex", "--output", path}, flags...)
		stdout, stderr, code, _ := executeCLI(t, args)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "--format") {
			t.Fatalf("flags %v: exit=%d stdout=%s stderr=%s", flags, code, stdout, stderr)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "keep" {
			t.Fatalf("invalid flags modified output: %q, %v", body, err)
		}
	}
	for _, destination := range []string{"", filepath.Join(t.TempDir(), "missing", "report.md"), t.TempDir()} {
		stdout, stderr, code, _ := executeCLI(t, []string{"verify", "--harness", "codex", "--format", "markdown", "--output", destination})
		if code != 2 || stdout != "" || !strings.Contains(stderr, "output") || strings.Contains(stderr, "Scanning") {
			t.Fatalf("bad output path: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	}
}

func TestVerifyJSONAliasAndMatchingFormat(t *testing.T) {
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	bin := t.TempDir()
	mustWriteFile(t, filepath.Join(bin, "codex"), "#!/bin/sh\nexit 9\n", 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, flags := range [][]string{{"--json"}, {"--format", "json"}, {"--json", "--format", "json"}} {
		args := append([]string{"verify", "--harness", "codex", repository}, flags...)
		stdout, stderr, code, log := executeCLI(t, args)
		t.Cleanup(func() { _ = os.Remove(log) })
		if code != 0 || !json.Valid([]byte(stdout)) {
			t.Fatalf("flags %v: exit=%d stdout=%s stderr=%s", flags, code, stdout, stderr)
		}
	}
}

func TestVerifyMarkdownCanceledRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root, state := newRootCommand("test")
	root.SetArgs([]string{"verify", "--harness", "codex", "--format", "markdown"})
	var body, stderr strings.Builder
	root.SetOut(&body)
	root.SetErr(&stderr)
	if code := executeRoot(ctx, root, state); code != 2 || !strings.Contains(body.String(), "Verification could not complete") {
		t.Fatalf("canceled run: exit=%d body=%s stderr=%s", code, body.String(), stderr.String())
	}
	t.Cleanup(func() { _ = os.Remove(state.verificationLogPath) })
}
