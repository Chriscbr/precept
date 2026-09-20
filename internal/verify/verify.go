package verify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
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
	events := &eventWriter{observe: options.Observe}

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

				if ctx.Err() == nil {
					events.emit(Event{Index: index, Claim: claims[index]})
				}
				outcome := runOne(ctx, claims[index], validator, options.Timeout)
				outcomes[index] = outcome
				events.emit(Event{Index: index, Claim: claims[index], Outcome: &outcome})
			}
		}()
	}
	workers.Wait()

	return outcomes, events.err()
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

// eventWriter serializes notifications without delaying completed claims behind
// earlier work. Rendering failures do not discard collected outcomes.
type eventWriter struct {
	mu       sync.Mutex
	observe  func(Event) error
	writeErr error
}

func (events *eventWriter) emit(event Event) {
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.observe != nil && events.writeErr == nil {
		events.writeErr = events.observe(event)
	}
}

func (events *eventWriter) err() error {
	if events.writeErr == nil {
		return nil
	}
	return fmt.Errorf("write verification progress: %w", events.writeErr)
}
