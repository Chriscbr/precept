// Package harness runs supported coding-agent CLIs in isolated, read-only
// non-interactive modes.
package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Chriscbr/precept/internal/prompt"
)

// Request contains the per-claim inputs shared by all harnesses.
type Request struct {
	RepositoryRoot string
	Prompt         string
	Model          string
	Effort         string
}

// Info describes an installed harness executable.
type Info struct {
	Name       string `json:"name"`
	Executable string `json:"executable"`
	Version    string `json:"version"`
}

// RunResult contains the normalized verifier result and bounded raw agent
// output. SessionID can be used with the selected agent's resume command.
// When Run returns an error, any fields captured before that error are still
// populated.
type RunResult struct {
	Result           prompt.Result `json:"result"`
	SessionID        string        `json:"session_id,omitempty"`
	ConversationPath string        `json:"conversation_path,omitempty"`
}

func findClaudeConversationPath(repositoryRoot, sessionID string) string {
	if !safeSessionID(sessionID) {
		return ""
	}
	configRoot := agentConfigRoot(repositoryRoot, "CLAUDE_CONFIG_DIR", ".claude")
	if configRoot == "" {
		return ""
	}

	projects, err := os.ReadDir(filepath.Join(configRoot, "projects"))
	if err != nil {
		return ""
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		candidate := filepath.Join(configRoot, "projects", project.Name(), sessionID+".jsonl")
		if regularFile(candidate) {
			return candidate
		}
	}
	return ""
}

func findCodexConversationPath(repositoryRoot, sessionID string) string {
	return findCodexConversationPathAt(repositoryRoot, sessionID, time.Now())
}

func findCodexConversationPathAt(repositoryRoot, sessionID string, now time.Time) string {
	if !safeSessionID(sessionID) {
		return ""
	}
	configRoot := agentConfigRoot(repositoryRoot, "CODEX_HOME", ".codex")
	if configRoot == "" {
		return ""
	}

	seenDates := make(map[string]struct{}, 6)
	for _, dayOffset := range []int{0, -1, 1} {
		for _, date := range []time.Time{now.AddDate(0, 0, dayOffset), now.UTC().AddDate(0, 0, dayOffset)} {
			datePath := date.Format("2006/01/02")
			if _, seen := seenDates[datePath]; seen {
				continue
			}
			seenDates[datePath] = struct{}{}
			if candidate := findCodexConversationOnDate(configRoot, datePath, sessionID); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func findCodexConversationOnDate(configRoot, datePath, sessionID string) string {
	directory := filepath.Join(configRoot, "sessions", filepath.FromSlash(datePath))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return ""
	}
	prefix := "rollout-"
	suffix := "-" + sessionID + ".jsonl"
	// os.ReadDir sorts by filename. Prefer the latest matching rollout if a
	// session unexpectedly has more than one file for the same date.
	for index := len(entries) - 1; index >= 0; index-- {
		name := entries[index].Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) || len(name) <= len(prefix)+len(suffix) {
			continue
		}
		candidate := filepath.Join(directory, name)
		if regularFile(candidate) {
			return candidate
		}
	}
	return ""
}

func safeSessionID(sessionID string) bool {
	if sessionID == "" || len(sessionID) > 255 || sessionID == "." || sessionID == ".." {
		return false
	}
	for _, character := range sessionID {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func agentConfigRoot(repositoryRoot, environmentVariable, fallbackDirectory string) string {
	root := os.Getenv(environmentVariable)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, fallbackDirectory)
	} else if !filepath.IsAbs(root) {
		root = filepath.Join(repositoryRoot, root)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return filepath.Clean(absolute)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

type decodedOutput struct {
	payload   []byte
	sessionID string
}

// Runner invokes one supported coding-agent CLI.
type Runner interface {
	Name() string
	Preflight(context.Context) (Info, error)
	Run(context.Context, Request) (RunResult, error)
}

// New constructs a runner for a supported agent name.
func New(name string) (Runner, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude":
		return &claudeRunner{executable: "claude"}, nil
	case "codex":
		return &codexRunner{executable: "codex"}, nil
	default:
		return nil, fmt.Errorf("unsupported agent %q (supported agents: claude, codex)", name)
	}
}
