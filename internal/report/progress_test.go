package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/verify"
	"github.com/charmbracelet/x/ansi"
)

func progressSettings() Settings { return Settings{Agent: "codex", Jobs: 4, Timeout: 10 * time.Minute} }

func TestProgressScanDelayCleanupAndDiagnostics(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	p := newProgress(&out, &stderr, time.Now(), FormatText)
	p.interactive = true
	if err := p.StartScan("src\x1b[2J"); err != nil {
		t.Fatal(err)
	}
	p.tick(p.phaseStarted.Add(scanDelay - time.Millisecond))
	if stderr.Len() != 0 {
		t.Fatalf("fast scan flickered: %q", stderr.String())
	}
	p.tick(p.phaseStarted.Add(scanDelay))
	if !strings.Contains(stderr.String(), `Scanning for claims in src\u{1b}[2J`) {
		t.Fatalf("scan status = %q", stderr.String())
	}
	if err := p.EndScan(discover.Result{Diagnostics: []discover.Diagnostic{{File: "broken.go", Line: 3, Message: "empty claim"}}}); err != nil {
		t.Fatal(err)
	}
	before := stderr.String()
	p.tick(time.Now().Add(time.Minute))
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != before || len(p.frame) != 0 {
		t.Fatal("completed scan kept repainting")
	}
	if !strings.HasSuffix(before, ansi.CursorUp(1)+"\r"+ansi.EraseScreenBelow+"warning broken.go:3: empty claim\n") {
		t.Fatalf("diagnostic was not preserved after clearing scan: %q", before)
	}
	if out.Len() != 0 {
		t.Fatal("progress contaminated stdout")
	}
}

func TestProgressAbortedScanOnlyPreservesVisibleStatus(t *testing.T) {
	t.Parallel()
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "fast", true: "visible"}[visible], func(t *testing.T) {
			var stderr bytes.Buffer
			p := newProgress(io.Discard, &stderr, time.Now(), FormatText)
			p.interactive = true
			if err := p.StartScan("example"); err != nil {
				t.Fatal(err)
			}
			if visible {
				p.tick(p.phaseStarted.Add(scanDelay))
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if !visible && stderr.Len() != 0 {
				t.Fatalf("aborted fast scan printed a status: %q", stderr.String())
			}
			if visible && !strings.HasSuffix(stderr.String(), ansi.EraseScreenBelow+"Scanning for claims in example\n") {
				t.Fatalf("visible aborted scan lost its context: %q", stderr.String())
			}
		})
	}
}

func TestProgressPlainHeartbeatAndCompletionBlocks(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	p := newProgress(&out, &stderr, time.Now(), FormatText)
	run := fixtureRun()
	run.Outcomes[0].Duration = 18 * time.Second
	run.Outcomes[1].Duration = 72 * time.Second
	run.FinishedAt = run.StartedAt.Add(72 * time.Second)
	if err := p.StartScan("example"); err != nil {
		t.Fatal(err)
	}
	if err := p.EndScan(discover.Result{Claims: []discover.Claim{run.Outcomes[0].Claim, run.Outcomes[1].Claim}}); err != nil {
		t.Fatal(err)
	}
	if err := p.StartVerification("example", 2, progressSettings()); err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		if err := p.Observe(verify.Event{Index: index, Claim: run.Outcomes[index].Claim}); err != nil {
			t.Fatal(err)
		}
	}
	before := stderr.String()
	p.tick(p.lastOutput.Add(heartbeatInterval - time.Millisecond))
	if stderr.String() != before {
		t.Fatal("heartbeat printed too soon")
	}
	p.tick(p.lastOutput.Add(heartbeatInterval))
	if !strings.Contains(stderr.String(), "Still verifying: 0 of 2 complete, 2 active, 0 queued") {
		t.Fatal(stderr.String())
	}
	for _, index := range []int{1, 0} {
		if err := p.Observe(verify.Event{Index: index, Claim: run.Outcomes[index].Claim, Outcome: &run.Outcomes[index]}); err != nil {
			t.Fatal(err)
		}
	}
	run.Outcomes = run.Outcomes[:2]
	if err := p.Finish(run); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Index(text, "(*Cache).Get") > strings.Index(text, "example.Clamp") {
		t.Fatal("blocks were buffered into source order")
	}
	if strings.Count(text, "(*Cache).Get [INVARIANT]") != 1 || strings.Count(text, "2 claims:") != 1 {
		t.Fatalf("duplicated output:\n%s", text)
	}
	for _, want := range []string{
		"Agent harness  codex",
		"Model          (default)", "Effort         (default)", "Extra context  (none)", "Timeout        10m per claim", "Exit code: 1",
		"example.Clamp [PRECONDITION] (clamp-precondition-1)  ✓ HOLDS (18s)",
		"(*Cache).Get [INVARIANT] (cache-miss)  ✗ VIOLATED (1m12s)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text+stderr.String(), "\x1b") || strings.Contains(text, "•") || strings.Contains(text, "·") {
		t.Fatal("plain output contains terminal markup or dot separators")
	}
}

func TestProgressJSONStaysCleanAndOrderedWithInteractiveStderr(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	p := newProgress(&out, &stderr, time.Now(), FormatJSON)
	p.interactive = true
	p.errStyle.enabled = true
	run := fixtureRun()
	if err := p.StartVerification("example", 4, progressSettings()); err != nil {
		t.Fatal(err)
	}
	for index := 3; index >= 0; index-- {
		if err := p.Observe(verify.Event{Index: index, Claim: run.Outcomes[index].Claim}); err != nil {
			t.Fatal(err)
		}
		if err := p.Observe(verify.Event{Index: index, Claim: run.Outcomes[index].Claim, Outcome: &run.Outcomes[index]}); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatal("JSON stream had intermediate output")
	}
	if err := p.Finish(run); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	var document jsonDocument
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	if err := WriteJSON(&expected, run); err != nil {
		t.Fatal(err)
	}
	if out.String() != expected.String() {
		t.Fatal("JSON differs from the deterministic report")
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(stderr.String(), "\x1b[") || len(p.frame) != 0 {
		t.Fatal("TTY handling did not stay isolated to stderr")
	}
}

func TestProgressInterruptedRunPreservesCompletedResults(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	p := newProgress(&out, &stderr, time.Now(), FormatText)
	run := fixtureRun()
	run.Outcomes = run.Outcomes[:2]
	run.Outcomes[1].Result, run.Outcomes[1].Error = nil, context.Canceled
	if err := p.StartVerification("example", 2, progressSettings()); err != nil {
		t.Fatal(err)
	}
	for index := range run.Outcomes {
		if err := p.Observe(verify.Event{Index: index, Outcome: &run.Outcomes[index]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Finish(run); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Interrupted: 1 of 2 claims completed; 1 unfinished.") || !strings.Contains(text, "1 holds") {
		t.Fatal(text)
	}
	if strings.Contains(text, "! ERROR") || strings.Contains(text, "(*Cache).Get") {
		t.Fatal("cancellation was presented as a claim verdict")
	}
}

func TestProgressJSONInterruptionReportsUnfinishedOnStderr(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	p := newProgress(&out, &stderr, time.Now(), FormatJSON)
	run := fixtureRun()
	run.Outcomes = run.Outcomes[:2]
	run.Outcomes[1].Result, run.Outcomes[1].Error = nil, context.Canceled
	if err := p.StartVerification("example", 2, progressSettings()); err != nil {
		t.Fatal(err)
	}
	for index := range run.Outcomes {
		if err := p.Observe(verify.Event{Index: index, Outcome: &run.Outcomes[index]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Finish(run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Interrupted: 1 of 2 claims completed; 1 unfinished.") {
		t.Fatalf("stderr omitted interruption summary: %q", stderr.String())
	}
	var document jsonDocument
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("interruption contaminated JSON: %v", err)
	}
	if len(document.Outcomes) != 2 || document.Outcomes[1].Error != context.Canceled.Error() {
		t.Fatalf("JSON lost the canceled claim: %+v", document.Outcomes)
	}
}

func TestProgressActiveChecksShareClaimHeadings(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	p := newProgress(io.Discard, &stderr, time.Now(), FormatText)
	p.interactive, p.errStyle.enabled = true, true
	if err := p.StartVerification("example", 3, progressSettings()); err != nil {
		t.Fatal(err)
	}
	claim := discover.Claim{ID: "clamp-precondition-1", Package: "example", Symbol: "Clamp", Marker: discover.MarkerPrecondition, File: "example/clamp.go", MarkerLine: 11}
	if err := p.Observe(verify.Event{Index: 0, Claim: claim}); err != nil {
		t.Fatal(err)
	}
	p.tick(time.Now())
	frame := ansi.Strip(strings.Join(p.frame, "\n"))
	for _, want := range []string{
		"  Active   1 check\n  Queued   2 checks\n  Elapsed  ",
		"  › example.Clamp [PRECONDITION] (clamp-precondition-1)\n    example/clamp.go:11  running ",
		"0 holds, 0 violated, 0 inconclusive, 0 errors",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProgressFramesFitNarrowAndShortTerminals(t *testing.T) {
	t.Parallel()
	for _, dimensions := range [][2]int{{80, 24}, {30, 8}, {8, 3}, {2, 2}} {
		var stderr bytes.Buffer
		p := newProgress(io.Discard, &stderr, time.Now(), FormatText)
		p.interactive = true
		p.size = func() (int, int) { return dimensions[0], dimensions[1] }
		if err := p.StartVerification("example", 12, progressSettings()); err != nil {
			t.Fatal(err)
		}
		for index := range 12 {
			if err := p.Observe(verify.Event{Index: index, Claim: discover.Claim{Symbol: "長い関数名👩‍💻", File: strings.Repeat("long/", 30) + "test.go", MarkerLine: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		p.tick(time.Now())
		if len(p.frame) >= dimensions[1] {
			t.Fatalf("frame exceeds height %d: %v", dimensions[1], p.frame)
		}
		for _, line := range p.frame {
			if ansi.StringWidth(line) >= dimensions[0] {
				t.Fatalf("line wraps at width %d: %q", dimensions[0], line)
			}
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProgressResizeDoesNotClearAboveKnownFooterRows(t *testing.T) {
	t.Parallel()
	for _, resized := range [][2]int{{20, 24}, {80, 4}, {20, 4}} {
		var stderr bytes.Buffer
		p := newProgress(io.Discard, &stderr, time.Now(), FormatText)
		p.interactive = true
		dimensions := [2]int{80, 24}
		p.size = func() (int, int) { return dimensions[0], dimensions[1] }
		if err := p.StartVerification("example", 4, progressSettings()); err != nil {
			t.Fatal(err)
		}
		oldRows := len(p.frame)
		stderr.Reset()
		dimensions = resized
		p.tick(time.Now())
		// Narrowing must not infer extra wrapped rows: terminals which clip
		// instead of reflowing would erase completed results above the footer.
		wantClear := ansi.CursorUp(min(oldRows, resized[1]-1)) + "\r" + ansi.EraseScreenBelow
		if !strings.HasPrefix(stderr.String(), wantClear) {
			t.Fatalf("resize to %v clears unknown rows: %q", resized, stderr.String())
		}
		if len(p.frame) >= resized[1] {
			t.Fatalf("resize to %v overflows the viewport: %v", resized, p.frame)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProgressSettingsOverridesAndContext(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	settings := progressSettings()
	settings.Model, settings.Effort = "custom-model", "high"
	settings.TextPrompts, settings.PromptFiles = 2, 1
	if err := writeSettings(&out, "src\x1b", 1, settings, textStyle{enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"custom-model", "high", "3 additions (2 text, 1 file)", `src\u{1b}`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out.String(), "(default)") {
		t.Fatal("explicit override labeled as default")
	}
}

type failedOutput struct{ err error }

func (w failedOutput) Write([]byte) (int, error) { return 0, w.err }

func TestProgressPropagatesWriteFailuresAndStops(t *testing.T) {
	t.Parallel()
	want := errors.New("closed output")
	p := newProgress(io.Discard, failedOutput{want}, time.Now(), FormatText)
	if err := p.StartScan("."); !errors.Is(err, want) {
		t.Fatalf("StartScan = %v", err)
	}
	if err := p.Close(); !errors.Is(err, want) {
		t.Fatalf("Close = %v", err)
	}
	if err := p.Close(); !errors.Is(err, want) {
		t.Fatalf("second Close = %v", err)
	}
}
