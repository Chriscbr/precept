package report

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Chriscbr/precept/internal/verify"
)

func TestWriteMarkdownGolden(t *testing.T) {
	t.Parallel()
	run := fixtureRun()
	run.SourceURLs = map[string]string{
		"example/cache.go": "https://github.com/example/repo/blob/abc123/example/cache.go",
	}
	assertGolden(t, "markdown.golden", func(buffer *bytes.Buffer) error {
		return WriteMarkdown(buffer, run)
	})
}

func TestMarkdownSortsByFileThenLineWithoutMutatingRun(t *testing.T) {
	t.Parallel()
	run := fixtureRun()
	run.Outcomes = run.Outcomes[:3]
	run.Outcomes[0].Claim.ID = "last-file"
	run.Outcomes[0].Claim.File = "z.go"
	run.Outcomes[1].Claim.ID = "later-line"
	run.Outcomes[1].Claim.File = "a.go"
	run.Outcomes[1].Claim.MarkerLine = 100
	run.Outcomes[2].Claim.ID = "earlier-line"
	run.Outcomes[2].Claim.File = "a.go"
	run.Outcomes[2].Claim.MarkerLine = 2
	before := append([]verify.Outcome(nil), run.Outcomes...)
	var output bytes.Buffer
	if err := WriteMarkdown(&output, run); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !(strings.Index(text, "`earlier-line`") < strings.Index(text, "`later-line`") && strings.Index(text, "`later-line`") < strings.Index(text, "`last-file`")) {
		t.Fatalf("summary was not sorted by file and numeric line:\n%s", text)
	}
	details := strings.Split(text, "### Failed claims")[1]
	if strings.Index(details, "`earlier-line`") > strings.Index(details, "`later-line`") || strings.Contains(details, "`last-file`") {
		t.Fatalf("failed claims are unordered or include holds:\n%s", details)
	}
	if !reflect.DeepEqual(run.Outcomes, before) {
		t.Fatal("Markdown reordered the input outcomes")
	}
}

func TestMarkdownEscapesSourceAndAgentText(t *testing.T) {
	t.Parallel()
	run := fixtureRun()
	run.Diagnostics = nil
	run.Outcomes = run.Outcomes[1:2]
	outcome := &run.Outcomes[0]
	outcome.Claim.ID = "id|`</details>"
	outcome.Claim.Text = "claim\n# injected heading <details>"
	outcome.Claim.Symbol = "`symbol`"
	outcome.Claim.File = "a|b.go"
	outcome.Result.Summary = "</details>\n# heading\n<script>\x1b[2J\n![image](https://example.com)"
	outcome.Result.Counterexample = "```\n</details>\n```"
	outcome.Result.Evidence[0].Reason = "first\n\n- not a list"
	var output bytes.Buffer
	if err := WriteMarkdown(&output, run); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"``id|`</details>``", "`a|b.go:42`",
		"claim \\# injected heading &lt;details&gt;", "`` example.`symbol` ``",
		"&lt;/details&gt;<br>\n\\# heading", "&lt;script&gt;", "first \\- not a list",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing escaped content %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "\n</details>\n") != 2 || strings.Contains(text, "\x1b") || strings.Contains(text, "\n# heading") || strings.Contains(text, "<script>") || strings.Contains(text, "![image]") {
		t.Fatalf("untrusted text changed report structure:\n%s", text)
	}
}

func TestMarkdownEmptySuccessAndRunFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		run  Run
		want string
	}{
		{"empty", Run{Scope: "empty"}, "No claims found in `empty`."},
		{"preflight error", Run{Scope: ".", Error: "agent missing"}, "**Verification could not complete.** agent missing"},
		{"success", Run{Scope: ".", Outcomes: fixtureRun().Outcomes[:1]}, "None. All claims hold."},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteMarkdown(&output, test.run); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("missing %q:\n%s", test.want, output.String())
			}
			if len(test.run.Outcomes) == 0 && strings.Contains(output.String(), "All claims hold") {
				t.Fatal("empty/failed run was reported as all claims holding")
			}
		})
	}
}

func TestMarkdownErrorOutcomes(t *testing.T) {
	t.Parallel()
	run := fixtureRun()
	run.Outcomes = run.Outcomes[:3]
	run.Outcomes[0].Result = nil
	run.Outcomes[1].Result.Verdict = "invalid"
	run.Outcomes[2].Result.Verdict = verify.VerdictError
	var output bytes.Buffer
	if err := WriteMarkdown(&output, run); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "#### ⚠️ Error:") != 3 || !strings.Contains(output.String(), "3 errors.") || strings.Count(output.String(), "**Supporting evidence:**") != 1 {
		t.Fatalf("invalid results or claim-type errors lost their explanation:\n%s", output.String())
	}
}

func TestMarkdownPropagatesWriteFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("output failed")
	if err := WriteMarkdown(failedOutput{want}, fixtureRun()); !errors.Is(err, want) {
		t.Fatalf("write error = %v, want %v", err, want)
	}
}

func TestMarkdownEvidenceLineRangeLink(t *testing.T) {
	t.Parallel()
	run := fixtureRun()
	run.Outcomes = run.Outcomes[2:3]
	run.SourceURLs = map[string]string{"example/config.go": "https://github.com/example/repo/blob/abc123/example/config.go"}
	var output bytes.Buffer
	if err := WriteMarkdown(&output, run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "[`example/config.go:22-24`](<https://github.com/example/repo/blob/abc123/example/config.go#L22-L24>)") {
		t.Fatalf("missing evidence permalink:\n%s", output.String())
	}
}
