package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

func isTerminalInput(input io.Reader) bool {
	file, ok := input.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

type verifyQuestion struct {
	flag   string
	label  string
	accept func(string) error
}

func promptVerifyOptions(ctx context.Context, input io.Reader, output io.Writer, options verifyFlags, flagProvided func(string) bool) (verifyFlags, error) {
	if err := ctx.Err(); err != nil {
		return options, err
	}
	questions := []verifyQuestion{
		{
			flag:  "agent",
			label: "Harness (1: Claude Code, 2: Codex): ",
			accept: func(answer string) error {
				switch strings.ToLower(answer) {
				case "1", "claude":
					options.agent = "claude"
				case "2", "codex":
					options.agent = "codex"
				default:
					return fmt.Errorf("choose 1 (claude) or 2 (codex)")
				}
				return nil
			},
		},
		{
			flag:  "model",
			label: "Model override [agent default]: ",
			accept: func(answer string) error {
				options.model = answer
				return nil
			},
		},
		{
			flag:  "effort",
			label: "Reasoning effort override [agent default]: ",
			accept: func(answer string) error {
				options.effort = answer
				return nil
			},
		},
		{
			flag:  "jobs",
			label: fmt.Sprintf("Concurrent workers [%d]: ", options.jobs),
			accept: func(answer string) error {
				if answer == "" {
					return nil
				}
				jobs, err := strconv.Atoi(answer)
				if err != nil || jobs <= 0 {
					return fmt.Errorf("enter a positive whole number")
				}
				options.jobs = jobs
				return nil
			},
		},
		{
			flag:  "timeout",
			label: fmt.Sprintf("Timeout per claim [%s]: ", options.timeout),
			accept: func(answer string) error {
				if answer == "" {
					return nil
				}
				timeout, err := time.ParseDuration(answer)
				if err != nil || timeout <= 0 {
					return fmt.Errorf("enter a positive duration, such as 30s or 10m")
				}
				options.timeout = timeout
				return nil
			},
		},
		{
			flag:  "json",
			label: "Output format (text/json) [text]: ",
			accept: func(answer string) error {
				switch strings.ToLower(answer) {
				case "", "text":
					options.jsonOutput = false
				case "json":
					options.jsonOutput = true
				default:
					return fmt.Errorf("choose text or json")
				}
				return nil
			},
		},
	}
	reader := bufio.NewReader(input)
	prompted := false
	for _, question := range questions {
		if flagProvided != nil && flagProvided(question.flag) {
			continue
		}
		if !prompted {
			if err := writeFormatted(output, "Select options. Press Enter to keep defaults; Ctrl+C to cancel.\n"); err != nil {
				return options, err
			}
			prompted = true
		}
		if err := askVerifyQuestion(ctx, reader, output, question); err != nil {
			return options, fmt.Errorf("interactive setup: %w", err)
		}
	}
	return options, ctx.Err()
}

func askVerifyQuestion(ctx context.Context, input *bufio.Reader, output io.Writer, question verifyQuestion) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeFormatted(output, "%s", question.label); err != nil {
			return err
		}
		answer, err := readVerifyAnswer(ctx, input)
		if err != nil {
			return err
		}
		if err := question.accept(strings.TrimSpace(answer)); err != nil {
			if writeErr := writeFormatted(output, "%s\n", err); writeErr != nil {
				return writeErr
			}
			continue
		}
		return nil
	}
}

func readVerifyAnswer(ctx context.Context, input *bufio.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		answer string
		err    error
	}
	// Terminal reads can block while Execute's signal handler cancels the context.
	// Keep the read separate so Ctrl+C exits even without a trailing newline. The
	// buffered channel lets the reader finish if input arrives after cancellation.
	done := make(chan result, 1)
	go func() {
		answer, err := input.ReadString('\n')
		done <- result{answer, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-done:
		if result.err != nil {
			return "", fmt.Errorf("read answer (setup canceled): %w", result.err)
		}
		return result.answer, ctx.Err()
	}
}
