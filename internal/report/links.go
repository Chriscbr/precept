package report

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chriscbr/precept/internal/textsafe"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

func newTextStyle(writer io.Writer, repositoryRoot string) textStyle {
	return textStyle{
		enabled:        shouldStyle(writer, os.LookupEnv),
		hyperlinks:     isInteractiveTerminal(writer, os.LookupEnv),
		repositoryRoot: repositoryRoot,
	}
}

func isInteractiveTerminal(writer io.Writer, lookupEnv func(string) (string, bool)) bool {
	terminal, _ := lookupEnv("TERM")
	if strings.EqualFold(strings.TrimSpace(terminal), "dumb") {
		return false
	}
	output, ok := writer.(fileDescriptorWriter)
	return ok && term.IsTerminal(int(output.Fd()))
}

// FileLink renders a path as a terminal hyperlink with an absolute file URL.
// Redirected output and dumb terminals receive only the sanitized path text.
func FileLink(writer io.Writer, path string) string {
	return newTextStyle(writer, "").linkPath(path, textsafe.SingleLine(path))
}

// App installation is not checked because the CLI may be running on a remote host.
func (style textStyle) codexSessionLink(sessionID string) string {
	const label = "View in ChatGPT"
	if !style.hyperlinks {
		return label
	}
	target := "codex://threads/" + url.PathEscape(strings.TrimSpace(sessionID))
	return ansi.SetHyperlink(target) + ansi.NewStyle().Underline(true).String() + label +
		ansi.NewStyle().Underline(false).String() + ansi.ResetHyperlink()
}

func (style textStyle) linkPath(path, label string) string {
	if !style.hyperlinks || path == "" {
		return label
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(style.repositoryRoot, filepath.FromSlash(path))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return label
	}
	// A Windows drive letter needs a leading slash in a file URL.
	urlPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	// URL encoding also prevents filenames from injecting terminal controls.
	target := (&url.URL{Scheme: "file", Path: urlPath}).String()
	return ansi.SetHyperlink(target) + label + ansi.ResetHyperlink()
}
