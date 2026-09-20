package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxAgentOutputBytes = 8 << 20
	maxErrorDetailBytes = 32 << 10
)

type commandOutput struct {
	stdout          []byte
	stderr          []byte
	stdoutTruncated bool
	stderrTruncated bool
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || written > 0
		return written, nil
	}
	if len(data) > remaining {
		_, _ = buffer.buffer.Write(data[:remaining])
		buffer.truncated = true
		return written, nil
	}
	_, _ = buffer.buffer.Write(data)
	return written, nil
}

func (buffer *boundedBuffer) bytes() []byte {
	return bytes.Clone(buffer.buffer.Bytes())
}

// preflight only locates the CLI. Starting the agent (including a --version
// probe) can hang or crash, so process failures belong to individual claims.
func preflight(ctx context.Context, name, executable string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return Info{}, fmt.Errorf("preflight %s: executable %q was not found in PATH: %w", name, executable, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Info{}, fmt.Errorf("preflight %s: resolve executable: %w", name, err)
	}
	return Info{Name: name, Executable: path}, nil
}

func validateRequest(request Request) (string, error) {
	if strings.TrimSpace(request.RepositoryRoot) == "" {
		return "", fmt.Errorf("repository root must not be empty")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}

	root, err := filepath.Abs(request.RepositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("inspect repository root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository root %q is not a directory", root)
	}
	return root, nil
}

func resolveExecutable(name, executable string) (string, error) {
	path, err := exec.LookPath(executable)
	if err != nil {
		return "", fmt.Errorf("run %s: executable %q was not found in PATH: %w", name, executable, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("run %s: resolve executable %q: %w", name, path, err)
	}
	return path, nil
}

func execute(ctx context.Context, executable string, args []string, directory, stdin string, limit int) (commandOutput, error) {
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.Stdin = strings.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 5 * time.Second
	configureCommandCancellation(command)

	err := command.Run()
	return commandOutput{
		stdout:          stdout.bytes(),
		stderr:          stderr.bytes(),
		stdoutTruncated: stdout.truncated,
		stderrTruncated: stderr.truncated,
	}, err
}

func processError(operation string, runErr, contextErr error, output commandOutput) error {
	if contextErr != nil {
		if errors.Is(contextErr, context.DeadlineExceeded) {
			return fmt.Errorf("%s timed out: %w", operation, contextErr)
		}
		return fmt.Errorf("%s canceled: %w", operation, contextErr)
	}

	detail, displayTruncated := boundedErrorDetail(output.stderr, maxErrorDetailBytes)
	detail = strings.TrimSpace(detail)
	if output.stderrTruncated {
		detail += fmt.Sprintf("\n[stderr capture truncated after %d bytes]", len(output.stderr))
	} else if displayTruncated {
		detail += fmt.Sprintf("\n[stderr display truncated to %d bytes]", maxErrorDetailBytes)
	}
	if detail == "" {
		return fmt.Errorf("%s failed: %w", operation, runErr)
	}
	return fmt.Errorf("%s failed: %w: %s", operation, runErr, detail)
}

func boundedErrorDetail(detail []byte, limit int) (string, bool) {
	if len(detail) <= limit {
		return string(detail), false
	}
	// Keep both ends: CLIs commonly print the root cause first and a concise
	// summary last.
	headSize := limit / 2
	tailSize := limit - headSize
	return string(detail[:headSize]) + "\n...\n" + string(detail[len(detail)-tailSize:]), true
}

func rejectTruncatedOutput(name string, output commandOutput) error {
	if output.stdoutTruncated {
		return fmt.Errorf("run %s: stdout exceeded the %d-byte capture limit", name, maxAgentOutputBytes)
	}
	return nil
}
