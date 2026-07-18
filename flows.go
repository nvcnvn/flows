package flows

// This package implements a minimal, Postgres-backed durable workflow runner.
//
// Key ideas:
// - Workflows run inside a caller-provided `pgx.Tx` for ACID semantics.
// - Each step result is memoized in Postgres keyed by (run_id, step_key).
// - "Blocking" primitives (Sleep / WaitForEvent) durably yield execution; the worker
//   later resumes by re-running the workflow, which replays and skips completed steps.

import (
	"context"
	"time"
)

// RunID identifies a workflow run.
type RunID string

// RunKey uniquely identifies a run within a sharded Postgres setup.
//
// In sharded deployments (e.g., Citus), all writes and updates are routed using
// (workflow_name_shard, run_id) to avoid scatter queries.
type RunKey struct {
	WorkflowNameShard string
	RunID             RunID
}

// Step is the type-safe unit of work within a workflow.
type Step[I any, O any] func(ctx context.Context, in *I) (*O, error)

// RetryPolicy controls in-process retries for a single step invocation.
//
// Note: retries happen inside a worker attempt; Durable memoization only stores
// successful outputs.
type RetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts after the initial attempt.
	// A value of 0 means no retries (only the initial attempt).
	MaxRetries int

	// Backoff returns the duration in milliseconds to wait before retry attempt n.
	// If nil, no backoff is applied between retries.
	Backoff func(attempt int) (durationMillis int)

	// StepTimeout is the maximum duration a single step execution may take.
	// If zero, no timeout is applied. When a step times out, it counts as a
	// failed attempt and may be retried according to MaxRetries.
	StepTimeout time.Duration
}

// RunRetryPolicy controls automatic re-execution of a run after the workflow
// function returns an error or panics.
//
// A retrying run is set back to sleeping with next_wake_at = now()+backoff and
// re-claimed like any other runnable run. Steps memoized by earlier attempts
// are replayed, so only the failing tail of the workflow re-executes (unless
// the failure aborted the attempt's transaction, in which case the whole
// attempt rolls back and replays).
//
// Errors wrapped with Terminal are never retried.
type RunRetryPolicy struct {
	// MaxAttempts is the total number of attempts (the initial execution plus
	// retries). Zero or one means no run-level retries: the first workflow
	// error fails the run.
	MaxAttempts int

	// Backoff returns the delay before the next attempt; attempt is the
	// 1-based number of attempts that have already failed. If nil, retries
	// are immediate.
	Backoff func(attempt int) time.Duration
}

func (p RunRetryPolicy) maxAttempts() int {
	if p.MaxAttempts <= 0 {
		return 1
	}
	return p.MaxAttempts
}

// TerminalError marks a workflow error as non-retryable. Use Terminal to wrap.
type TerminalError struct {
	Err error
}

func (e *TerminalError) Error() string { return e.Err.Error() }

func (e *TerminalError) Unwrap() error { return e.Err }

// Terminal wraps err so the run fails immediately, bypassing any
// RunRetryPolicy. Use it for errors that will never succeed on retry
// (validation failures, business rejections).
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &TerminalError{Err: err}
}

// ExponentialBackoff returns a Backoff function for RunRetryPolicy that
// doubles the delay per failed attempt, starting at base and capped at max.
func ExponentialBackoff(base, max time.Duration) func(attempt int) time.Duration {
	return func(attempt int) time.Duration {
		if base <= 0 {
			return 0
		}
		d := base
		for i := 1; i < attempt; i++ {
			d *= 2
			if max > 0 && d >= max {
				return max
			}
		}
		if max > 0 && d > max {
			d = max
		}
		return d
	}
}

// Workflow is a durable workflow definition.
//
// The runtime re-executes the workflow function on resume. Use Context primitives
// (Execute/Sleep/WaitForEvent/Random*) to get replay-safe behavior.
type Workflow[I any, O any] interface {
	Name() string
	Run(ctx context.Context, wf *Context, in *I) (*O, error)
}

// DurableExecute is an alias for Execute.
func DurableExecute[I any, O any](ctx context.Context, wf *Context, stepKey string, step Step[I, O], in *I, retryPolicy RetryPolicy) (*O, error) {
	return Execute(ctx, wf, stepKey, step, in, retryPolicy)
}
