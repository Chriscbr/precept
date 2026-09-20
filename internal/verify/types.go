// Package verify runs claim validations with bounded concurrency.
package verify

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Chriscbr/precept/internal/discover"
)

// Verdict is the semantic outcome of validating a claim.
type Verdict string

const (
	// VerdictHolds means the validator found sufficient evidence that the claim holds.
	VerdictHolds Verdict = "holds"
	// VerdictViolated means the validator found a concrete violation of the claim.
	VerdictViolated Verdict = "violated"
	// VerdictInconclusive means the validator could not establish either other verdict.
	VerdictInconclusive Verdict = "inconclusive"
	// VerdictError means the claim marker is strictly incompatible with the claim.
	VerdictError Verdict = "error"
)

// Valid reports whether verdict is part of Precept's result contract.
func (verdict Verdict) Valid() bool {
	switch verdict {
	case VerdictHolds, VerdictViolated, VerdictInconclusive, VerdictError:
		return true
	default:
		return false
	}
}

// Evidence identifies source lines that support a validation result.
type Evidence struct {
	File      string
	StartLine int
	EndLine   int
	Reason    string
}

// Result is the normalized, harness-independent result of validating one claim.
type Result struct {
	Verdict        Verdict
	Summary        string
	Evidence       []Evidence
	Counterexample string
}

// Validate checks the structural invariants shared by all harness results.
func (result Result) Validate() error {
	if !result.Verdict.Valid() {
		return fmt.Errorf("invalid verdict %q", result.Verdict)
	}
	if strings.TrimSpace(result.Summary) == "" {
		return errors.New("summary is empty")
	}
	if len(result.Evidence) == 0 {
		return errors.New("evidence is empty")
	}
	for index, evidence := range result.Evidence {
		if err := validateRepositoryPath(evidence.File); err != nil {
			return fmt.Errorf("evidence %d file: %w", index+1, err)
		}
		if evidence.StartLine <= 0 {
			return fmt.Errorf("evidence %d has invalid start line %d", index+1, evidence.StartLine)
		}
		if evidence.EndLine < evidence.StartLine {
			return fmt.Errorf(
				"evidence %d ends on line %d before its start line %d",
				index+1,
				evidence.EndLine,
				evidence.StartLine,
			)
		}
		if strings.TrimSpace(evidence.Reason) == "" {
			return fmt.Errorf("evidence %d has an empty reason", index+1)
		}
	}
	if result.Verdict != VerdictViolated && strings.TrimSpace(result.Counterexample) != "" {
		return fmt.Errorf("counterexample is only valid for a %q verdict", VerdictViolated)
	}
	return nil
}

func validateRepositoryPath(file string) error {
	if strings.TrimSpace(file) == "" {
		return errors.New("is empty")
	}
	if strings.Contains(file, "\\") {
		return errors.New("must use forward slashes")
	}
	if strings.HasPrefix(file, "/") {
		return errors.New("must be repository-relative")
	}
	cleaned := path.Clean(file)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.New("must remain inside the repository")
	}
	if cleaned != file {
		return errors.New("must be a clean repository-relative path")
	}
	return nil
}

// Validation contains the semantic result and agent-session metadata produced by
// one validator invocation. Result may be zero when Validate also returns an error,
// but SessionID remains meaningful and is preserved in the resulting Outcome.
type Validation struct {
	Result    Result
	SessionID string
}

// Validator validates exactly one claim. Implementations must stop promptly when
// ctx is canceled; this lets Run enforce its concurrency and timeout bounds.
type Validator interface {
	Validate(ctx context.Context, claim discover.Claim) (Validation, error)
}

// Outcome records either a normalized result or an operational error for one claim.
// Result is nil when Error is non-nil.
type Outcome struct {
	Claim     discover.Claim
	Result    *Result
	Error     error
	Duration  time.Duration
	SessionID string
}

// Options controls bounded validation execution.
type Options struct {
	Jobs    int
	Timeout time.Duration
	// Observe receives serialized start and completion events in real time.
	// A nil Outcome marks a start; completed outcomes retain discovery order in
	// Run's return value, independently of notification order.
	Observe func(Event) error
}

// Event identifies a claim starting or completing verification.
type Event struct {
	Index   int
	Claim   discover.Claim
	Outcome *Outcome
}

// TimeoutError indicates that one validation exceeded its configured deadline.
type TimeoutError struct {
	Timeout time.Duration
}

// Error implements error.
func (err *TimeoutError) Error() string {
	return fmt.Sprintf("validation timed out after %s", err.Timeout)
}

// Unwrap permits errors.Is(err, context.DeadlineExceeded).
func (err *TimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}
