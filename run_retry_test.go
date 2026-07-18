package flows_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	flows "github.com/nvcnvn/flows"
)

// flakyRunWorkflow fails the whole run until FailUntil attempts have happened.
// step1 always succeeds so memoization across run-level retries can be verified.
type flakyRunWorkflow struct {
	Step1Calls atomic.Int32
	RunCalls   atomic.Int32
	FailUntil  int32
	Terminal   bool
}

func (w *flakyRunWorkflow) Name() string { return "flaky_run_workflow" }

func (w *flakyRunWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	attempt := w.RunCalls.Add(1)

	_, err := flows.Execute(ctx, wf, "step1", func(ctx context.Context, in *SimpleInput) (*SimpleInput, error) {
		w.Step1Calls.Add(1)
		return in, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	if attempt <= w.FailUntil {
		err := fmt.Errorf("transient failure on attempt %d", attempt)
		if w.Terminal {
			return nil, flows.Terminal(err)
		}
		return nil, err
	}
	return &SimpleOutput{Greeting: "recovered"}, nil
}

func startRun[I any, O any](t *testing.T, pool *pgxpool.Pool, wf flows.Workflow[I, O], in *I) flows.RunKey {
	t.Helper()
	ctx := context.Background()
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	runKey, err := flows.BeginTx(ctx, client, tx, wf, in)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return runKey
}

// TestRunRetry_SucceedsAfterRetries verifies a run that fails transiently is
// retried until it succeeds, that memoized steps are not re-executed across
// run-level retries, and that the attempts counter is recorded.
func TestRunRetry_SucceedsAfterRetries(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &flakyRunWorkflow{FailUntil: 2}
	flows.Register(registry, wf, flows.WithRunRetry(flows.RunRetryPolicy{MaxAttempts: 5}))

	runKey := startRun(t, pool, wf, &SimpleInput{Name: "retry"})

	worker := newTestWorker(pool, registry)
	for i := 0; i < 3; i++ {
		if !processOneEventually(t, worker, 5*time.Second) {
			t.Fatalf("expected attempt %d to be claimable", i+1)
		}
	}
	waitForRunStatus(t, pool, runKey, "completed", 2*time.Second)

	client := flows.Client{}
	status, err := flows.GetRunStatusTx(ctx, client, pool, runKey)
	if err != nil {
		t.Fatalf("GetRunStatusTx: %v", err)
	}
	if status.Attempts != 2 {
		t.Errorf("expected 2 failed attempts recorded, got %d", status.Attempts)
	}
	if status.Error != "" {
		t.Errorf("expected error_text cleared on success, got %q", status.Error)
	}
	if calls := wf.RunCalls.Load(); calls != 3 {
		t.Errorf("expected workflow to run 3 times, got %d", calls)
	}
	// step1 succeeded on attempt 1 and committed with the retry state, so
	// later attempts must replay it from memoization.
	if calls := wf.Step1Calls.Load(); calls != 1 {
		t.Errorf("expected step1 to execute once across retries, got %d", calls)
	}
}

// TestRunRetry_ExhaustsAttempts verifies the run fails once MaxAttempts is
// reached and is not claimed again.
func TestRunRetry_ExhaustsAttempts(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &flakyRunWorkflow{FailUntil: 100}
	flows.Register(registry, wf, flows.WithRunRetry(flows.RunRetryPolicy{MaxAttempts: 2}))

	runKey := startRun(t, pool, wf, &SimpleInput{Name: "doomed"})

	worker := newTestWorker(pool, registry)
	for i := 0; i < 2; i++ {
		if !processOneEventually(t, worker, 5*time.Second) {
			t.Fatalf("expected attempt %d to be claimable", i+1)
		}
	}
	waitForRunStatus(t, pool, runKey, "failed", 2*time.Second)

	client := flows.Client{}
	status, err := flows.GetRunStatusTx(ctx, client, pool, runKey)
	if err != nil {
		t.Fatalf("GetRunStatusTx: %v", err)
	}
	if status.Attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", status.Attempts)
	}
	if status.Error == "" {
		t.Error("expected error_text on failed run")
	}

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne after failure: %v", err)
	}
	if processed {
		t.Error("failed run must not be claimed again")
	}
}

// TestRunRetry_TerminalErrorSkipsRetry verifies Terminal-wrapped errors fail
// the run immediately even when a retry policy allows more attempts.
func TestRunRetry_TerminalErrorSkipsRetry(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &flakyRunWorkflow{FailUntil: 100, Terminal: true}
	flows.Register(registry, wf, flows.WithRunRetry(flows.RunRetryPolicy{MaxAttempts: 5}))

	runKey := startRun(t, pool, wf, &SimpleInput{Name: "rejected"})

	worker := newTestWorker(pool, registry)
	if !processOneEventually(t, worker, 5*time.Second) {
		t.Fatal("expected the run to be claimed")
	}
	waitForRunStatus(t, pool, runKey, "failed", 2*time.Second)

	client := flows.Client{}
	status, err := flows.GetRunStatusTx(ctx, client, pool, runKey)
	if err != nil {
		t.Fatalf("GetRunStatusTx: %v", err)
	}
	if status.Attempts != 1 {
		t.Errorf("expected exactly 1 attempt, got %d", status.Attempts)
	}
	if calls := wf.RunCalls.Load(); calls != 1 {
		t.Errorf("expected workflow to run once, got %d", calls)
	}
}

// TestRunRetry_BackoffDelaysNextAttempt verifies a retrying run is parked
// until its backoff elapses (evaluated on the database clock).
func TestRunRetry_BackoffDelaysNextAttempt(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &flakyRunWorkflow{FailUntil: 100}
	flows.Register(registry, wf, flows.WithRunRetry(flows.RunRetryPolicy{
		MaxAttempts: 3,
		Backoff:     func(attempt int) time.Duration { return 30 * time.Second },
	}))

	runKey := startRun(t, pool, wf, &SimpleInput{Name: "parked"})

	worker := newTestWorker(pool, registry)
	if !processOneEventually(t, worker, 5*time.Second) {
		t.Fatal("expected the run to be claimed")
	}
	waitForRunStatus(t, pool, runKey, "sleeping", 2*time.Second)

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne during backoff: %v", err)
	}
	if processed {
		t.Error("run must not be re-claimed before its retry backoff elapses")
	}

	client := flows.Client{}
	status, err := flows.GetRunStatusTx(ctx, client, pool, runKey)
	if err != nil {
		t.Fatalf("GetRunStatusTx: %v", err)
	}
	if status.Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", status.Attempts)
	}
	if status.NextWakeAt == nil {
		t.Error("expected NextWakeAt to be set for a pending retry")
	}
}

// abortingRetryWorkflow aborts the workflow transaction on the first attempt
// and succeeds afterwards.
type abortingRetryWorkflow struct {
	RunCalls atomic.Int32
}

func (w *abortingRetryWorkflow) Name() string { return "aborting_retry_workflow" }

func (w *abortingRetryWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	if w.RunCalls.Add(1) == 1 {
		if _, err := wf.Tx().Exec(ctx, "INSERT INTO this_table_does_not_exist VALUES (1)"); err != nil {
			return nil, err
		}
		return nil, errors.New("expected SQL failure")
	}
	return &SimpleOutput{Greeting: "recovered after abort"}, nil
}

// TestRunRetry_AbortedTxStillRetries verifies the fresh-transaction fallback
// records the retry (not a terminal failure) when the workflow aborted its
// own transaction.
func TestRunRetry_AbortedTxStillRetries(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &abortingRetryWorkflow{}
	flows.Register(registry, wf, flows.WithRunRetry(flows.RunRetryPolicy{MaxAttempts: 3}))

	runKey := startRun(t, pool, wf, &SimpleInput{Name: "abort-then-recover"})

	worker := newTestWorker(pool, registry)
	for i := 0; i < 2; i++ {
		if !processOneEventually(t, worker, 5*time.Second) {
			t.Fatalf("expected attempt %d to be claimable", i+1)
		}
	}
	waitForRunStatus(t, pool, runKey, "completed", 2*time.Second)

	client := flows.Client{}
	status, err := flows.GetRunStatusTx(ctx, client, pool, runKey)
	if err != nil {
		t.Fatalf("GetRunStatusTx: %v", err)
	}
	if status.Attempts != 1 {
		t.Errorf("expected 1 failed attempt, got %d", status.Attempts)
	}
	if calls := wf.RunCalls.Load(); calls != 2 {
		t.Errorf("expected workflow to run twice, got %d", calls)
	}
}

func TestExponentialBackoff(t *testing.T) {
	backoff := flows.ExponentialBackoff(100*time.Millisecond, time.Second)
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("attempt %d: expected %v, got %v", i+1, w, got)
		}
	}
}
