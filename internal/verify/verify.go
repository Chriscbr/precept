package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"precept/internal/discover"
	"precept/internal/textsafe"
)

// Run validates claims with at most options.Jobs concurrent calls to validator.
// Outcomes preserve input order even when validations complete out of order.
//
// If ctx is canceled, in-flight validators receive that cancellation and claims
// that have not started receive cancellation outcomes without invoking validator.
func Run(
	ctx context.Context,
	claims []discover.Claim,
	validator Validator,
	options Options,
) ([]Outcome, error) {
	if ctx == nil {
		return nil, errors.New("verification context is nil")
	}
	if validator == nil {
		return nil, errors.New("validator is nil")
	}
	if options.Jobs <= 0 {
		return nil, fmt.Errorf("jobs must be positive, got %d", options.Jobs)
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive, got %s", options.Timeout)
	}

	outcomes := make([]Outcome, len(claims))
	if len(claims) == 0 {
		return outcomes, nil
	}

	workerCount := min(options.Jobs, len(claims))
	progress := newProgressWriter(options.Progress, outcomes)

	var claimMu sync.Mutex
	next := 0
	claim := func() (int, bool) {
		claimMu.Lock()
		defer claimMu.Unlock()
		if next == len(claims) {
			return 0, false
		}
		index := next
		next++
		return index, true
	}

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				index, ok := claim()
				if !ok {
					return
				}

				outcome := runOne(ctx, claims[index], validator, options.Timeout)
				outcomes[index] = outcome
				progress.complete(index)
			}
		}()
	}
	workers.Wait()

	return outcomes, progress.err()
}

func runOne(
	parent context.Context,
	claim discover.Claim,
	validator Validator,
	timeout time.Duration,
) Outcome {
	startedAt := time.Now()
	outcome := Outcome{Claim: claim}
	if err := parent.Err(); err != nil {
		outcome.Error = err
		outcome.Duration = time.Since(startedAt)
		return outcome
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	validation, validationErr := validator.Validate(ctx, claim)
	contextErr := ctx.Err()
	cancel()

	outcome.Duration = time.Since(startedAt)
	outcome.SessionID = strings.TrimSpace(validation.SessionID)
	switch {
	case errors.Is(contextErr, context.DeadlineExceeded):
		outcome.Error = &TimeoutError{Timeout: timeout}
	case errors.Is(contextErr, context.Canceled):
		outcome.Error = context.Canceled
	case validationErr != nil:
		outcome.Error = validationErr
	default:
		if err := validation.Result.Validate(); err != nil {
			outcome.Error = fmt.Errorf("invalid validation result: %w", err)
		} else {
			outcome.Result = &validation.Result
		}
	}
	return outcome
}

// progressWriter serializes writes and delays completed entries only as necessary
// to emit them in input order. This keeps progress deterministic without coupling
// worker completion order to final reporting.
type progressWriter struct {
	mu       sync.Mutex
	writer   io.Writer
	outcomes []Outcome
	done     []bool
	next     int
	writeErr error
}

func newProgressWriter(
	writer io.Writer,
	outcomes []Outcome,
) *progressWriter {
	return &progressWriter{
		writer:   writer,
		outcomes: outcomes,
		done:     make([]bool, len(outcomes)),
	}
}

func (progress *progressWriter) complete(index int) {
	progress.mu.Lock()
	defer progress.mu.Unlock()

	progress.done[index] = true
	if progress.writer == nil || progress.writeErr != nil {
		return
	}
	for progress.next < len(progress.done) && progress.done[progress.next] {
		outcome := progress.outcomes[progress.next]
		status := "error"
		if outcome.Error == nil && outcome.Result != nil {
			status = string(outcome.Result.Verdict)
		}
		_, progress.writeErr = fmt.Fprintf(
			progress.writer,
			"[%d/%d] %s %s:%d %s\n",
			progress.next+1,
			len(progress.done),
			status,
			textsafe.SingleLine(outcome.Claim.File),
			outcome.Claim.MarkerLine,
			textsafe.SingleLine(outcome.Claim.Symbol),
		)
		if progress.writeErr != nil {
			return
		}
		progress.next++
	}
}

func (progress *progressWriter) err() error {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if progress.writeErr == nil {
		return nil
	}
	return fmt.Errorf("write verification progress: %w", progress.writeErr)
}
