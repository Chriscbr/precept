package report

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/textsafe"
	"github.com/Chriscbr/precept/internal/verify"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

const (
	scanDelay         = 200 * time.Millisecond
	heartbeatInterval = 30 * time.Second
)

// Settings contains the human-readable invocation settings. Model and effort
// are overrides, not guesses about the agent's resolved defaults.
type Settings struct {
	Agent       string
	Model       string
	Effort      string
	Jobs        int
	Timeout     time.Duration
	TextPrompts int
	PromptFiles int
}

type activeClaim struct {
	claim   discover.Claim
	started time.Time
}

// Progress owns the transient stderr display and coordinates it with durable
// stdout blocks. JSON is written only once, at Finish, in discovery order.
type Progress struct {
	mu                                sync.Mutex
	out, errOut                       io.Writer
	outStyle, errStyle                textStyle
	interactive, json                 bool
	size                              func() (int, int)
	started, phaseStarted, lastOutput time.Time
	phase, scope, agent               string
	scanVisible                       bool
	total, canceled                   int
	active                            map[int]activeClaim
	completed                         []verify.Outcome
	frame                             []string
	step                              int
	err                               error
	stop, done                        chan struct{}
	closeOnce                         sync.Once
}

func newProgress(out, errOut io.Writer, started time.Time, jsonOutput bool) *Progress {
	p := &Progress{
		out: out, errOut: errOut, started: started, lastOutput: started,
		json: jsonOutput, active: make(map[int]activeClaim),
		outStyle: textStyle{enabled: shouldStyle(out, os.LookupEnv)},
		errStyle: textStyle{enabled: shouldStyle(errOut, os.LookupEnv)},
		size:     func() (int, int) { return 80, 24 },
	}
	if fd, ok := errOut.(fileDescriptorWriter); ok {
		p.interactive = term.IsTerminal(int(fd.Fd())) && !strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb")
		p.size = func() (int, int) {
			width, height, err := term.GetSize(int(fd.Fd()))
			if err != nil || width < 2 || height < 2 {
				return 80, 24
			}
			return width, height
		}
	}
	return p
}

// NewProgress starts a bounded refresh loop. Call Close on every exit path.
func NewProgress(out, errOut io.Writer, started time.Time, jsonOutput bool) *Progress {
	p := newProgress(out, errOut, started, jsonOutput)
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(125 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				p.tick(now)
			case <-p.stop:
				return
			}
		}
	}()
	return p
}

// Close stops animation before the caller prints any final operational error.
func (p *Progress) Close() error {
	p.closeOnce.Do(func() {
		if p.stop != nil {
			close(p.stop)
			<-p.done
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.clearFrame()
		if p.phase == "scan" && p.interactive && p.scanVisible {
			p.write(p.errOut, "Scanning for claims in %s\n", textsafe.SingleLine(p.scope))
		}
		p.phase = ""
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Progress) StartScan(scope string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scope, p.phase, p.phaseStarted = scope, "scan", time.Now()
	p.scanVisible = false
	if !p.interactive {
		p.write(p.errOut, "Scanning for claims in %s\n", textsafe.SingleLine(scope))
	}
	return p.err
}

func (p *Progress) EndScan(result discover.Result) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearFrame()
	if !p.interactive {
		files := make(map[string]struct{})
		for _, claim := range result.Claims {
			files[claim.File] = struct{}{}
		}
		p.write(p.errOut, "Found %d %s in %d %s (%s)\n", len(result.Claims), plural(len(result.Claims), "claim", "claims"),
			len(files), plural(len(files), "file", "files"), elapsed(time.Since(p.phaseStarted)))
	}
	p.phase = ""
	if p.err == nil {
		p.record(writeDiagnostics(p.errOut, result.Diagnostics))
	}
	return p.err
}

func (p *Progress) StartVerification(scope string, total int, settings Settings) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scope, p.total, p.agent = scope, total, settings.Agent
	if total == 0 {
		writer := p.out
		if p.json {
			writer = p.errOut
		}
		p.write(writer, "No claims found in %s\n", textsafe.SingleLine(scope))
		return p.err
	}
	if !p.json {
		p.record(writeSettings(p.out, scope, total, settings, p.outStyle))
	}
	p.phase, p.phaseStarted, p.lastOutput = "verify", time.Now(), time.Now()
	if p.interactive {
		p.render(time.Now())
	}
	return p.err
}

func writeSettings(writer io.Writer, scope string, total int, settings Settings, style textStyle) error {
	if _, err := fmt.Fprintf(writer, "%s\n\n", style.bold(fmt.Sprintf("Verifying %d %s in %s", total, plural(total, "claim", "claims"), textsafe.SingleLine(scope)))); err != nil {
		return err
	}
	context := style.gray("(none)")
	if count := settings.TextPrompts + settings.PromptFiles; count > 0 {
		context = fmt.Sprintf("%d %s (%d text, %d %s)", count, plural(count, "addition", "additions"), settings.TextPrompts, settings.PromptFiles, plural(settings.PromptFiles, "file", "files"))
	}
	defaultValue := func(value string) string {
		if value == "" {
			return style.gray("(default)")
		}
		return textsafe.SingleLine(value)
	}
	timeout := settings.Timeout.String()
	if settings.Timeout%time.Minute == 0 {
		timeout = fmt.Sprintf("%dm", settings.Timeout/time.Minute)
	}
	for _, field := range [][2]string{
		{"Agent", textsafe.SingleLine(settings.Agent)},
		{"Model", defaultValue(settings.Model)},
		{"Effort", defaultValue(settings.Effort)},
		{"Workers", fmt.Sprintf("up to %d", settings.Jobs)},
		{"Timeout", timeout + " per claim"},
		{"Context", context},
	} {
		if _, err := fmt.Fprintf(writer, "  %s%s\n", style.gray(fmt.Sprintf("%-12s", field[0])), field[1]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer)
	return err
}

// Observe is safe to call from workers. Completed blocks are emitted once, in
// completion order. Cancellation is summarized as unfinished work, not a verdict.
func (p *Progress) Observe(event verify.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Outcome == nil {
		p.active[event.Index] = activeClaim{claim: event.Claim, started: time.Now()}
		return p.err
	}
	delete(p.active, event.Index)
	outcome := *event.Outcome
	if errors.Is(outcome.Error, context.Canceled) {
		p.canceled++
		return p.err
	}
	p.completed = append(p.completed, outcome)
	p.clearFrame()
	if p.err == nil {
		if p.json {
			if !p.interactive {
				_, status, _ := outcomeStatus(outcome)
				p.write(p.errOut, "Completed %d of %d: %s %s (%s:%d)\n", len(p.completed), p.total, status,
					textsafe.SingleLine(displaySymbol(outcome.Claim)), textsafe.SingleLine(outcome.Claim.File), outcome.Claim.MarkerLine)
			}
		} else {
			p.record(writeTextOutcome(p.out, p.agent, outcome, p.outStyle))
			p.write(p.out, "\n")
		}
	}
	p.lastOutput = time.Now()
	if p.interactive {
		p.render(time.Now())
	}
	return p.err
}

func (p *Progress) Finish(run Run) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearFrame()
	p.phase = ""
	if p.err != nil {
		return p.err
	}
	if p.canceled > 0 {
		writer := p.out
		if p.json {
			writer = p.errOut
		}
		p.write(writer, "Interrupted: %d of %d claims completed; %d unfinished.\n", len(p.completed), p.total, p.canceled)
	}
	if p.err != nil {
		return p.err
	}
	if p.json {
		p.record(WriteJSON(p.out, run))
	} else if p.total > 0 {
		p.record(writeSummary(p.out, Summarize(p.completed), p.outStyle))
		p.write(p.out, "Finished in %s. Exit code: %d\n", elapsed(run.FinishedAt.Sub(run.StartedAt)), ExitCode(run.Outcomes))
	}
	return p.err
}

func (p *Progress) tick(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil || p.phase == "" {
		return
	}
	if p.interactive {
		if p.phase == "scan" && now.Sub(p.phaseStarted) < scanDelay {
			return
		}
		p.render(now)
	} else if now.Sub(p.lastOutput) >= heartbeatInterval {
		if p.phase == "scan" {
			p.write(p.errOut, "Still scanning for claims in %s (elapsed %s)\n", textsafe.SingleLine(p.scope), elapsed(now.Sub(p.phaseStarted)))
		} else {
			p.write(p.errOut, "Still verifying: %d of %d complete, %d active, %d queued (elapsed %s)\n", len(p.completed), p.total, len(p.active), p.queued(), elapsed(now.Sub(p.started)))
		}
		p.lastOutput = now
	}
}

func (p *Progress) queued() int { return max(0, p.total-len(p.completed)-p.canceled-len(p.active)) }

func (p *Progress) render(now time.Time) {
	if p.err != nil {
		return
	}
	p.clearFrame()
	width, height := p.size()
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	spinner := string(frames[p.step%len(frames)])
	p.step++
	var lines []string
	if p.phase == "scan" {
		lines = []string{p.errStyle.gray(fmt.Sprintf("%s Scanning for claims in %s  %s", spinner, textsafe.SingleLine(p.scope), elapsed(now.Sub(p.phaseStarted))))}
	} else if p.phase == "verify" {
		lines = p.verificationFrame(now, spinner, max(1, height-1))
	}
	lines = lines[:min(len(lines), max(1, height-1))]
	for _, line := range lines {
		line = ansi.Truncate(line, max(1, width-1), strings.Repeat(".", min(3, max(1, width-1))))
		p.write(p.errOut, "%s\n", line)
		if p.err != nil {
			return
		}
		p.frame = append(p.frame, line)
		if p.phase == "scan" {
			p.scanVisible = true
		}
	}
}

func (p *Progress) verificationFrame(now time.Time, spinner string, limit int) []string {
	style := p.errStyle
	lines := []string{
		fmt.Sprintf("%s  %d of %d claims complete", style.color(ansiCyan, spinner+" Verifying"), len(p.completed), p.total),
		fmt.Sprintf("  %s%d %s", style.gray("Active   "), len(p.active), plural(len(p.active), "check", "checks")),
		fmt.Sprintf("  %s%d %s", style.gray("Queued   "), p.queued(), plural(p.queued(), "check", "checks")),
		"  " + style.gray("Elapsed  ") + elapsed(now.Sub(p.started)),
	}
	indices := make([]int, 0, len(p.active))
	for index := range p.active {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	// Reserve a summary row, a blank separator, and a row for omitted checks.
	available := limit - len(lines) - 1
	shown := min(4, len(indices))
	for shown > 0 {
		required := 1 + 2*shown
		if shown < len(indices) {
			required++
		}
		if required <= available {
			break
		}
		shown--
	}
	if shown > 0 {
		lines = append(lines, "")
	}
	for _, index := range indices[:shown] {
		active := p.active[index]
		lines = append(lines,
			"  "+style.color(ansiCyan, "›")+" "+style.bold(textsafe.SingleLine(displaySymbol(active.claim)))+" "+
				style.color(ansiCyan, "["+textsafe.SingleLine(string(active.claim.Marker))+"]"),
			style.gray(fmt.Sprintf("    %s:%d  running %s", textsafe.SingleLine(active.claim.File), active.claim.MarkerLine, elapsed(now.Sub(active.started)))),
		)
	}
	if shown < len(indices) && len(lines) < limit-1 {
		lines = append(lines, style.gray(fmt.Sprintf("  ... %d more active checks", len(indices)-shown)))
	}
	if shown > 0 && len(lines)+1 < limit {
		lines = append(lines, "")
	}
	if len(lines) < limit {
		summary := Summarize(p.completed)
		lines = append(lines, strings.Join([]string{
			style.color(ansiGreen, fmt.Sprintf("%d holds", summary.Holds)),
			style.color(ansiRed, fmt.Sprintf("%d violated", summary.Violated)),
			style.color(ansiYellow, fmt.Sprintf("%d inconclusive", summary.Inconclusive)),
			style.color(ansiRed, fmt.Sprintf("%d %s", summary.Errors, plural(summary.Errors, "error", "errors"))),
		}, ", "))
	}
	return lines
}

func (p *Progress) clearFrame() {
	if len(p.frame) == 0 {
		return
	}
	_, height := p.size()
	// Each rendered line occupies one row. After a resize, terminals disagree
	// about reflow, so estimating extra rows from the new width can erase durable
	// output above the footer. Only clear rows known to belong to the frame and
	// still visible; a resize can leave older transient rows in scrollback.
	rows := min(len(p.frame), max(1, height-1))
	p.write(p.errOut, "%s\r%s", ansi.CursorUp(rows), ansi.EraseScreenBelow)
	p.frame = nil
}

func (p *Progress) write(writer io.Writer, format string, args ...any) {
	if p.err != nil {
		return
	}
	_, err := fmt.Fprintf(writer, format, args...)
	p.record(err)
	p.lastOutput = time.Now()
}

func (p *Progress) record(err error) {
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("write command output: %w", err)
	}
}

func elapsed(duration time.Duration) string {
	if duration < time.Second {
		return "<1s"
	}
	seconds := int(duration / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh%02dm%02ds", seconds/3600, seconds/60%60, seconds%60)
}
