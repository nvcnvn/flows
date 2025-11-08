package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/nvcnvn/flows/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type RetryInput struct {
	AttemptToSucceed int `json:"attempt_to_succeed"`
}

type RetryOutput struct {
	Success  bool `json:"success"`
	Attempts int  `json:"attempts"`
}

// Counter for tracking retry attempts
var attemptCounter int32

// RetryableActivity fails until a certain attempt
var RetryableActivity = flows.NewActivity(
	"retryable-activity",
	func(ctx context.Context, input *RetryInput) (*RetryOutput, error) {
		attempt := atomic.AddInt32(&attemptCounter, 1)

		if int(attempt) < input.AttemptToSucceed {
			return nil, errors.New("transient error - will retry")
		}

		return &RetryOutput{
			Success:  true,
			Attempts: int(attempt),
		}, nil
	},
	flows.RetryPolicy{
		InitialInterval: 100 * time.Millisecond,
		BackoffFactor:   2.0,
		MaxInterval:     1 * time.Second,
		MaxAttempts:     5,
		Jitter:          0.0, // No jitter for predictable testing
	},
)

// RetryWorkflow tests activity retry
var RetryWorkflow = flows.New(
	"retry-workflow",
	1,
	func(ctx *flows.Context[RetryInput]) (*RetryOutput, error) {
		input := ctx.Input()
		result, err := flows.ExecuteActivity(ctx, RetryableActivity, input)
		return result, err
	},
)

func TestActivityRetry_Success(t *testing.T) {
	ctx := context.Background()

	// Setup database
	pool := testutil.SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Set tenant
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Reset counter
	atomic.StoreInt32(&attemptCounter, 0)

	// Start workflow that should succeed on 3rd attempt
	exec, err := flows.Start(ctx, RetryWorkflow, &RetryInput{
		AttemptToSucceed: 3,
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"retry-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenantID,
	})

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for result
	resultChan := make(chan struct {
		result *RetryOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *RetryOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.True(t, res.result.Success)
		assert.Equal(t, 3, res.result.Attempts, "Should have succeeded on 3rd attempt")
		t.Logf("Activity succeeded after %d attempts", res.result.Attempts)
	case <-time.After(20 * time.Second):
		t.Fatal("Workflow did not complete within timeout")
	}

	worker.Stop()
}

func TestActivityRetry_MaxAttemptsExceeded(t *testing.T) {
	ctx := context.Background()

	// Setup database
	pool := testutil.SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Set tenant
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Reset counter
	atomic.StoreInt32(&attemptCounter, 0)

	// Start workflow that requires 10 attempts (more than max of 5)
	exec, err := flows.Start(ctx, RetryWorkflow, &RetryInput{
		AttemptToSucceed: 10,
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"retry-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenantID,
	})

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for result
	resultChan := make(chan struct {
		result *RetryOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *RetryOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		assert.Error(t, res.err, "Should fail after max attempts")
		t.Logf("Workflow failed as expected: %v", res.err)

		// Verify the workflow is in DLQ
		// TODO: Add DLQ query when available
	case <-time.After(30 * time.Second):
		t.Fatal("Workflow did not complete/fail within timeout")
	}

	worker.Stop()
}

// TerminalErrorActivity fails with non-retryable error
var TerminalErrorActivity = flows.NewActivity(
	"terminal-error-activity",
	func(ctx context.Context, input *RetryInput) (*RetryOutput, error) {
		return nil, flows.NewTerminalError(errors.New("this is a terminal error"))
	},
	flows.DefaultRetryPolicy,
)

var TerminalErrorWorkflow = flows.New(
	"terminal-error-workflow",
	1,
	func(ctx *flows.Context[RetryInput]) (*RetryOutput, error) {
		input := ctx.Input()
		result, err := flows.ExecuteActivity(ctx, TerminalErrorActivity, input)
		return result, err
	},
)

func TestActivityRetry_TerminalError(t *testing.T) {
	ctx := context.Background()

	// Setup database
	pool := testutil.SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Set tenant
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Start workflow
	exec, err := flows.Start(ctx, TerminalErrorWorkflow, &RetryInput{})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"terminal-error-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenantID,
	})

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for result
	resultChan := make(chan struct {
		result *RetryOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *RetryOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		assert.Error(t, res.err, "Should fail immediately with terminal error")
		assert.Contains(t, res.err.Error(), "terminal error", "Error should indicate it's terminal")
		t.Logf("Workflow failed immediately as expected: %v", res.err)
	case <-time.After(10 * time.Second):
		t.Fatal("Workflow should have failed quickly with terminal error")
	}

	worker.Stop()
}
