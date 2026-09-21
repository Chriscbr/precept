package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Chriscbr/precept/internal/textsafe"
	"github.com/Chriscbr/precept/internal/verify"
)

// WriteMarkdown renders one complete GitHub-flavored Markdown document. Sorting
// a copy keeps the source/discovery order of other report formats unchanged.
func WriteMarkdown(writer io.Writer, run Run) error {
	outcomes := append([]verify.Outcome(nil), run.Outcomes...)
	sort.SliceStable(outcomes, func(i, j int) bool {
		if outcomes[i].Claim.File != outcomes[j].Claim.File {
			return outcomes[i].Claim.File < outcomes[j].Claim.File
		}
		return outcomes[i].Claim.MarkerLine < outcomes[j].Claim.MarkerLine
	})
	summary := Summarize(outcomes)
	var body strings.Builder
	body.WriteString("## Precept verification\n\n")
	if run.Error != "" {
		fmt.Fprintf(&body, "**Verification could not complete.** %s\n\n", markdownText(run.Error))
	}
	if summary.Total == 0 {
		if run.Error == "" {
			fmt.Fprintf(&body, "No claims found in %s.\n\n", markdownCode(run.Scope))
		} else {
			fmt.Fprintf(&body, "No claim results are available for %s.\n\n", markdownCode(run.Scope))
		}
	} else {
		fmt.Fprintf(&body, "Out of %d total %s in %s, agents found %d holds, %d violated, %d inconclusive, and %d %s.\n\n",
			summary.Total, plural(summary.Total, "claim", "claims"), markdownCode(run.Scope),
			summary.Holds, summary.Violated, summary.Inconclusive, summary.Errors, plural(summary.Errors, "error", "errors"))
	}
	if len(run.Diagnostics) > 0 {
		body.WriteString("### Warnings\n\n")
		for _, diagnostic := range run.Diagnostics {
			fmt.Fprintf(&body, "- %s — %s\n", markdownLocation(run, diagnostic.File, diagnostic.Line, diagnostic.Line), markdownInline(diagnostic.Message))
		}
		body.WriteByte('\n')
	}
	body.WriteString("### Summary\n\n<details>\n<summary>All claims</summary>\n\n")
	for _, outcome := range outcomes {
		fmt.Fprintf(&body, "- %s - %s - %s\n", markdownStatus(outcome), markdownCode(outcome.Claim.ID),
			markdownLocation(run, outcome.Claim.File, outcome.Claim.MarkerLine, outcome.Claim.MarkerLine))
	}
	body.WriteString("\n</details>\n\n### Failed claims\n\n")
	if summary.Holds == summary.Total {
		if run.Error != "" {
			body.WriteString("Verification did not complete; see the error above.\n")
		} else if summary.Total == 0 {
			body.WriteString("No claims were verified.\n")
		} else {
			body.WriteString("None. All claims hold.\n")
		}
	} else {
		body.WriteString("<details>\n<summary>Failed claims</summary>\n\n")
		for _, outcome := range outcomes {
			if operationalError(outcome) == "" && outcome.Result.Verdict == verify.VerdictHolds {
				continue
			}
			fmt.Fprintf(&body, "#### %s: %s\n\n", markdownStatus(outcome), markdownInline(outcome.Claim.Text))
			fmt.Fprintf(&body, "%s · %s · %s · %s  \nSource: %s\n\n", markdownCode(outcome.Claim.ID), markdownInline(string(outcome.Claim.Marker)),
				markdownCode(displaySymbol(outcome.Claim)), markdownInline(elapsed(outcome.Duration)), markdownLocation(run, outcome.Claim.File, outcome.Claim.MarkerLine, outcome.Claim.MarkerLine))
			if reason := operationalError(outcome); reason != "" {
				fmt.Fprintf(&body, "%s\n\n", markdownText(reason))
				continue
			}
			fmt.Fprintf(&body, "%s\n\n", markdownText(outcome.Result.Summary))
			if strings.TrimSpace(outcome.Result.Counterexample) != "" {
				fmt.Fprintf(&body, "**Counterexample:** %s\n\n", markdownText(outcome.Result.Counterexample))
			}
			body.WriteString("**Supporting evidence:**\n\n")
			for _, evidence := range outcome.Result.Evidence {
				fmt.Fprintf(&body, "- %s — %s\n", markdownLocation(run, evidence.File, evidence.StartLine, evidence.EndLine), markdownInline(evidence.Reason))
			}
			body.WriteByte('\n')
		}
		body.WriteString("</details>\n")
	}
	if _, err := io.WriteString(writer, body.String()); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	return nil
}

func markdownStatus(outcome verify.Outcome) string {
	_, status, _ := outcomeStatus(outcome)
	switch status {
	case "HOLDS":
		return "✅ Holds"
	case "VIOLATED":
		return "❌ Violated"
	case "INCONCLUSIVE":
		return "❓ Inconclusive"
	default:
		return "⚠️ Error"
	}
}

func markdownLocation(run Run, file string, start, end int) string {
	label := file
	if start > 0 {
		label += fmt.Sprintf(":%d", start)
		if end > start {
			label += fmt.Sprintf("-%d", end)
		}
	}
	label = markdownCode(label)
	if target := run.SourceURLs[file]; target != "" {
		if start > 0 {
			target += fmt.Sprintf("#L%d", start)
			if end > start {
				target += fmt.Sprintf("-L%d", end)
			}
		}
		return "[" + label + "](<" + target + ">)"
	}
	return label
}

// Escape prose rather than interpreting agent output as Markdown. In particular,
// HTML and newlines must not close a details block or introduce new headings.
var markdownEscapes = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "`", "\\`",
	"*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "#", "\\#",
	"|", "\\|", "~", "\\~", "!", "\\!", "-", "\\-", "+", "\\+", ".", "\\.",
)

func markdownText(value string) string {
	lines := strings.Split(textsafe.Sanitize(value), "\n")
	for index, line := range lines {
		lines[index] = markdownEscapes.Replace(strings.TrimSpace(line))
	}
	return strings.Join(lines, "<br>\n")
}

func markdownInline(value string) string {
	return markdownText(strings.Join(strings.Fields(value), " "))
}

func markdownCode(value string) string {
	value = textsafe.SingleLine(value)
	longest, current := 0, 0
	for _, character := range value {
		if character == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	delimiter := strings.Repeat("`", longest+1)
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		value = " " + value + " "
	}
	return delimiter + value + delimiter
}
