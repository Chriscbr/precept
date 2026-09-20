package verify

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
)

type validatorFunc func(context.Context, discover.Claim) (Validation, error)

func (fn validatorFunc) Validate(ctx context.Context, claim discover.Claim) (Validation, error) {
	return fn(ctx, claim)
}

func validResult(summary string) Result {
	return Result{
		Verdict: VerdictHolds,
		Summary: summary,
		Evidence: []Evidence{{
			File:      "example/example.go",
			StartLine: 1,
			EndLine:   1,
			Reason:    "test evidence",
		}},
	}
}

func claims(count int) []discover.Claim {
	result := make([]discover.Claim, count)
	for index := range result {
		result[index] = discover.Claim{
			Marker:     discover.MarkerInvariant,
			Text:       fmt.Sprintf("claim %d", index),
			Package:    "example",
			Kind:       "func",
			Symbol:     fmt.Sprintf("Function%d", index),
			File:       "example/example.go",
			MarkerLine: index + 10,
			StartLine:  index + 11,
			EndLine:    index + 12,
		}
	}
	return result
}

func TestRunPreservesInputOrderWithBoundedConcurrency(t *testing.T) {
	t.Parallel()

	const jobs = 3
	var active atomic.Int64
	var maximum atomic.Int64
	var calls atomic.Int64
	validator := validatorFunc(func(_ context.Context, claim discover.Claim) (Validation, error) {
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if observed >= current || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		// Reverse durations to ensure completion order differs from input order.
		time.Sleep(time.Duration(12-claim.MarkerLine) * time.Millisecond)
		return Validation{Result: validResult(claim.Text), SessionID: "session-" + claim.Symbol}, nil
	})

	input := claims(10)
	outcomes, err := Run(context.Background(), input, validator, Options{
		Jobs:    jobs,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := calls.Load(); got != int64(len(input)) {
		t.Fatalf("validator calls = %d, want %d", got, len(input))
	}
	if got := maximum.Load(); got > jobs {
		t.Fatalf("maximum concurrency = %d, want <= %d", got, jobs)
	}
	if got := maximum.Load(); got < 2 {
		t.Fatalf("maximum concurrency = %d, test did not exercise concurrency", got)
	}
	for index, outcome := range outcomes {
		if outcome.Claim.Text != input[index].Text {
			t.Errorf("outcome %d claim = %q, want %q", index, outcome.Claim.Text, input[index].Text)
		}
		if outcome.Error != nil {
			t.Errorf("outcome %d error = %v", index, outcome.Error)
			continue
		}
		if outcome.Result == nil || outcome.Result.Summary != input[index].Text {
			t.Errorf("outcome %d result = %#v, want summary %q", index, outcome.Result, input[index].Text)
		}
		if want := "session-" + input[index].Symbol; outcome.SessionID != want {
			t.Errorf("outcome %d session ID = %q, want %q", index, outcome.SessionID, want)
		}
	}
}

func TestRunAppliesIndependentTimeout(t *testing.T) {
	t.Parallel()

	validator := validatorFunc(func(ctx context.Context, claim discover.Claim) (Validation, error) {
		<-ctx.Done()
		return Validation{SessionID: "  timeout-" + claim.Symbol + "  "}, ctx.Err()
	})
	outcomes, err := Run(context.Background(), claims(2), validator, Options{
		Jobs:    2,
		Timeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for index, outcome := range outcomes {
		if !errors.Is(outcome.Error, context.DeadlineExceeded) {
			t.Errorf("outcome %d error = %v, want deadline exceeded", index, outcome.Error)
		}
		var timeoutErr *TimeoutError
		if !errors.As(outcome.Error, &timeoutErr) {
			t.Errorf("outcome %d error type = %T, want *TimeoutError", index, outcome.Error)
		}
		wantSessionID := fmt.Sprintf("timeout-Function%d", index)
		if outcome.SessionID != wantSessionID {
			t.Errorf("outcome %d session ID = %q, want %q", index, outcome.SessionID, wantSessionID)
		}
	}
}

func TestRunDoesNotStartValidationAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int64
	validator := validatorFunc(func(context.Context, discover.Claim) (Validation, error) {
		calls.Add(1)
		return Validation{Result: validResult("unexpected")}, nil
	})

	outcomes, err := Run(ctx, claims(20), validator, Options{Jobs: 4, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("validator calls = %d, want 0", got)
	}
	for index, outcome := range outcomes {
		if !errors.Is(outcome.Error, context.Canceled) {
			t.Errorf("outcome %d error = %v, want context canceled", index, outcome.Error)
		}
	}
}

func TestRunCancelsInflightValidationAndDoesNotDispatchMoreWork(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	var calls atomic.Int64
	validator := validatorFunc(func(ctx context.Context, _ discover.Claim) (Validation, error) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return Validation{}, ctx.Err()
	})

	type runResult struct {
		outcomes []Outcome
		err      error
	}
	finished := make(chan runResult, 1)
	go func() {
		outcomes, err := Run(ctx, claims(20), validator, Options{Jobs: 2, Timeout: time.Minute})
		finished <- runResult{outcomes: outcomes, err: err}
	}()

	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("validator did not start")
	}

	var result runResult
	select {
	case result = <-finished:
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	if got := calls.Load(); got < 1 || got > 2 {
		t.Fatalf("validator calls = %d, want between 1 and 2", got)
	}
	for index, outcome := range result.outcomes {
		if !errors.Is(outcome.Error, context.Canceled) {
			t.Errorf("outcome %d error = %v, want context canceled", index, outcome.Error)
		}
	}
}

func TestRunRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	validator := validatorFunc(func(context.Context, discover.Claim) (Validation, error) {
		return Validation{Result: validResult("valid")}, nil
	})
	tests := []struct {
		name      string
		ctx       context.Context
		validator Validator
		options   Options
	}{
		{name: "nil context", validator: validator, options: Options{Jobs: 1, Timeout: time.Second}},
		{name: "nil validator", ctx: context.Background(), options: Options{Jobs: 1, Timeout: time.Second}},
		{name: "zero jobs", ctx: context.Background(), validator: validator, options: Options{Timeout: time.Second}},
		{name: "negative jobs", ctx: context.Background(), validator: validator, options: Options{Jobs: -1, Timeout: time.Second}},
		{name: "zero timeout", ctx: context.Background(), validator: validator, options: Options{Jobs: 1}},
		{name: "negative timeout", ctx: context.Background(), validator: validator, options: Options{Jobs: 1, Timeout: -time.Second}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Run(test.ctx, nil, test.validator, test.options); err == nil {
				t.Fatal("Run() error = nil, want an option validation error")
			}
		})
	}
}

func TestRunTurnsInvalidResultIntoOperationalError(t *testing.T) {
	t.Parallel()

	validator := validatorFunc(func(context.Context, discover.Claim) (Validation, error) {
		return Validation{Result: Result{Verdict: "unknown", Summary: "not valid"}}, nil
	})
	outcomes, err := Run(context.Background(), claims(1), validator, Options{Jobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcomes[0].Error == nil {
		t.Fatal("outcome error = nil, want invalid result error")
	}
	if outcomes[0].Result != nil {
		t.Fatalf("outcome result = %#v, want nil", outcomes[0].Result)
	}
}

func TestRunPreservesSessionIDOnValidatorError(t *testing.T) {
	t.Parallel()

	wantError := errors.New("agent output was malformed")
	validator := validatorFunc(func(context.Context, discover.Claim) (Validation, error) {
		return Validation{SessionID: "error-session"}, wantError
	})
	outcomes, err := Run(context.Background(), claims(1), validator, Options{Jobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !errors.Is(outcomes[0].Error, wantError) {
		t.Fatalf("outcome error = %v, want %v", outcomes[0].Error, wantError)
	}
	if outcomes[0].SessionID != "error-session" {
		t.Fatalf("outcome session ID = %q, want error-session", outcomes[0].SessionID)
	}
	if outcomes[0].Result != nil {
		t.Fatalf("outcome result = %#v, want nil", outcomes[0].Result)
	}
}

func TestRunEmitsCompletionsImmediatelyAndPreservesOutcomeOrder(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	releaseFirst := make(chan struct{})
	var order []int
	var started = make(map[int]bool)
	validator := validatorFunc(func(ctx context.Context, claim discover.Claim) (Validation, error) {
		if claim.MarkerLine == 10 {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return Validation{}, ctx.Err()
			}
		}
		return Validation{Result: validResult(claim.Symbol)}, nil
	})
	outcomes, err := Run(ctx, claims(2), validator, Options{
		Jobs: 2, Timeout: time.Second,
		Observe: func(event Event) error {
			if event.Outcome == nil {
				started[event.Index] = true
				return nil
			}
			if !started[event.Index] {
				t.Errorf("completion before start: %d", event.Index)
			}
			order = append(order, event.Index)
			if event.Index == 1 {
				close(releaseFirst)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 0 {
		t.Fatalf("completion order = %v", order)
	}
	for index, outcome := range outcomes {
		if outcome.Result == nil || outcome.Result.Summary != claims(2)[index].Symbol {
			t.Fatalf("outcome %d = %+v", index, outcome)
		}
	}
}

func TestRunReturnsProgressWriterErrorAfterValidation(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	validator := validatorFunc(func(context.Context, discover.Claim) (Validation, error) {
		calls.Add(1)
		return Validation{Result: validResult("valid")}, nil
	})
	outcomes, err := Run(context.Background(), claims(4), validator, Options{
		Jobs:    2,
		Timeout: time.Second,
		Observe: func(Event) error { return errors.New("closed") },
	})
	if err == nil {
		t.Fatal("Run() error = nil, want progress writer error")
	}
	if len(outcomes) != 4 {
		t.Fatalf("len(outcomes) = %d, want 4", len(outcomes))
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("validator calls = %d, want 4", got)
	}
}
