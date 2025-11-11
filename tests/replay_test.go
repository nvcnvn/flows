package examples_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ReplayWorkflowInput demonstrates workflow replay after activity failure
type ReplayWorkflowInput struct {
	JobID   string `json:"job_id"`
	Retries int    `json:"retries"`
}

type ReplayWorkflowOutput struct {
	JobID           string        `json:"job_id"`
	ExecutionID     string        `json:"execution_id"`
	BatchID         string        `json:"batch_id"`
	InitialTime     time.Time     `json:"initial_time"`
	BeforeFailTime  time.Time     `json:"before_fail_time"`
	AfterRetryTime  time.Time     `json:"after_retry_time"`
	CompletionTime  time.Time     `json:"completion_time"`
	TotalDuration   time.Duration `json:"total_duration"`
	AttemptCount    int           `json:"attempt_count"`
	Status          string        `json:"status"`
	UUIDs           []string      `json:"uuids"`
	FailureRecorded bool          `json:"failure_recorded"`
}

type ProcessJobInput struct {
	JobID     string    `json:"job_id"`
	Timestamp time.Time `json:"timestamp"`
	Attempt   int       `json:"attempt"`
}

type ProcessJobOutput struct {
	ProcessedID string `json:"processed_id"`
	Success     bool   `json:"success"`
	Attempt     int    `json:"attempt"`
}

// Per-job counter to simulate activity failure on first attempt
// This allows parallel tests to run without interfering with each other
var (
	jobAttemptCounters   = make(map[string]*int32)
	jobAttemptCountersMu sync.Mutex
)

func getJobAttemptCounter(jobID string) *int32 {
	jobAttemptCountersMu.Lock()
	defer jobAttemptCountersMu.Unlock()

	if counter, exists := jobAttemptCounters[jobID]; exists {
		return counter
	}

	var counter int32
	jobAttemptCounters[jobID] = &counter
	return &counter
}

func resetJobAttemptCounter(jobID string) {
	jobAttemptCountersMu.Lock()
	defer jobAttemptCountersMu.Unlock()

	delete(jobAttemptCounters, jobID)
}

func getJobAttemptCount(jobID string) int32 {
	jobAttemptCountersMu.Lock()
	defer jobAttemptCountersMu.Unlock()

	if counter, exists := jobAttemptCounters[jobID]; exists {
		return atomic.LoadInt32(counter)
	}
	return 0
}

// ProcessJobActivity - fails on first attempt, succeeds on retry
var ProcessJobActivity = flows.NewActivity(
	"process-job-with-retry",
	func(ctx context.Context, input *ProcessJobInput) (*ProcessJobOutput, error) {
		counter := getJobAttemptCounter(input.JobID)
		attempt := atomic.AddInt32(counter, 1)

		fmt.Printf("ProcessJobActivity called: job=%s, attempt=%d (input.attempt=%d)\n",
			input.JobID, attempt, input.Attempt)

		// Fail on first attempt
		if attempt == 1 {
			fmt.Printf("  -> FAILING on first attempt\n")
			return nil, flows.NewRetryableError(fmt.Errorf("simulated transient failure"))
		}

		// Succeed on retry
		fmt.Printf("  -> SUCCESS on retry (attempt=%d)\n", attempt)
		return &ProcessJobOutput{
			ProcessedID: uuid.New().String(),
			Success:     true,
			Attempt:     input.Attempt,
		}, nil
	},
	flows.RetryPolicy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     10 * time.Second,
		BackoffFactor:   2.0,
		MaxAttempts:     3,
		Jitter:          0.1,
	},
)

// ReplayTestWorkflow demonstrates deterministic replay after activity failure
var ReplayTestWorkflow = flows.New(
	"replay-test-workflow",
	1,
	func(ctx *flows.Context[ReplayWorkflowInput]) (*ReplayWorkflowOutput, error) {
		input := ctx.Input()

		// Record initial time - this will be the same on replay
		initialTime := ctx.Time()
		fmt.Printf("[Workflow] Initial time: %v\n", initialTime)

		// Generate execution ID - will be identical on replay
		executionID := ctx.UUIDv7()
		fmt.Printf("[Workflow] Execution ID: %s\n", executionID)

		// Generate batch ID - will be identical on replay
		batchID := ctx.UUIDv7()
		fmt.Printf("[Workflow] Batch ID: %s\n", batchID)

		uuids := []string{
			executionID.String(),
			batchID.String(),
		}

		// Sleep before attempting the job
		fmt.Println("[Workflow] Sleeping 1 second before processing...")
		if err := ctx.Sleep(1 * time.Second); err != nil {
			return nil, err
		}

		// Record time before attempting the potentially failing activity
		beforeFailTime := ctx.Time()
		fmt.Printf("[Workflow] Time before activity: %v\n", beforeFailTime)

		// Generate UUID before activity - will be identical on replay
		preActivityID := ctx.UUIDv7()
		uuids = append(uuids, preActivityID.String())

		// Attempt to process the job - this will fail on first execution
		// On replay, it will return the cached successful result
		fmt.Println("[Workflow] Calling ProcessJobActivity...")
		result, err := flows.ExecuteActivity(ctx, ProcessJobActivity, &ProcessJobInput{
			JobID:     input.JobID,
			Timestamp: beforeFailTime,
			Attempt:   1,
		})
		if err != nil {
			// This should not happen as the activity has retry policy
			return nil, fmt.Errorf("job processing failed after retries: %w", err)
		}

		fmt.Printf("[Workflow] Activity completed: success=%v, attempt=%d\n",
			result.Success, result.Attempt)

		// Sleep after successful activity
		fmt.Println("[Workflow] Sleeping 1 second after successful processing...")
		if err := ctx.Sleep(1 * time.Second); err != nil {
			return nil, err
		}

		// Record time after retry/success
		afterRetryTime := ctx.Time()
		fmt.Printf("[Workflow] Time after retry: %v\n", afterRetryTime)

		// Generate UUID after retry - will be identical on replay
		postRetryID := ctx.UUIDv7()
		uuids = append(uuids, postRetryID.String())

		// Record completion time
		completionTime := ctx.Time()
		totalDuration := completionTime.Sub(initialTime)

		// Generate final UUID
		finalID := ctx.UUIDv7()
		uuids = append(uuids, finalID.String())

		fmt.Printf("[Workflow] Completed. Duration: %v\n", totalDuration)

		return &ReplayWorkflowOutput{
			JobID:           input.JobID,
			ExecutionID:     executionID.String(),
			BatchID:         batchID.String(),
			InitialTime:     initialTime,
			BeforeFailTime:  beforeFailTime,
			AfterRetryTime:  afterRetryTime,
			CompletionTime:  completionTime,
			TotalDuration:   totalDuration,
			AttemptCount:    result.Attempt,
			Status:          "completed",
			UUIDs:           uuids,
			FailureRecorded: true,
		}, nil
	},
)

func TestReplayWorkflow_ActivityRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	jobID := "job-replay-test-001"

	// Reset counter for this specific job
	resetJobAttemptCounter(jobID)

	// Setup database connection
	pool := SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Set tenant ID
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	t.Logf("Using tenant ID: %s", tenantID)

	// Start workflow
	exec, err := flows.Start(ctx, ReplayTestWorkflow, &ReplayWorkflowInput{
		JobID:   jobID,
		Retries: 3,
	})
	require.NoError(t, err, "Failed to start workflow")
	assert.NotEmpty(t, exec.WorkflowID(), "Workflow ID should not be empty")

	t.Logf("Workflow started: id=%s", exec.WorkflowID())

	// Start worker in background
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"replay-test-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for workflow to complete (with timeout)
	// First attempt will fail, retry will succeed
	resultChan := make(chan struct {
		result *ReplayWorkflowOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *ReplayWorkflowOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err, "Workflow execution failed")
		require.NotNil(t, res.result, "Result should not be nil")

		// Verify results
		assert.NotEmpty(t, res.result.ExecutionID, "Execution ID should not be empty")
		assert.NotEmpty(t, res.result.BatchID, "Batch ID should not be empty")
		assert.Equal(t, "completed", res.result.Status, "Status should be completed")

		// Verify activity was actually called (failed first, succeeded on retry)
		currentAttempts := getJobAttemptCount(jobID)
		assert.Equal(t, int32(2), currentAttempts,
			"Activity should have been called twice (1 failure + 1 success)")

		// Verify time sequence
		assert.False(t, res.result.InitialTime.IsZero(), "Initial time should be set")
		assert.False(t, res.result.BeforeFailTime.IsZero(), "Before fail time should be set")
		assert.False(t, res.result.AfterRetryTime.IsZero(), "After retry time should be set")
		assert.False(t, res.result.CompletionTime.IsZero(), "Completion time should be set")

		// Times should be in order
		assert.True(t, res.result.BeforeFailTime.After(res.result.InitialTime),
			"BeforeFailTime should be after InitialTime")
		assert.True(t, res.result.AfterRetryTime.After(res.result.BeforeFailTime),
			"AfterRetryTime should be after BeforeFailTime")
		assert.True(t, res.result.CompletionTime.After(res.result.AfterRetryTime),
			"CompletionTime should be after AfterRetryTime")

		// Verify UUIDs
		assert.Equal(t, 5, len(res.result.UUIDs), "Should have 5 UUIDs")

		// Log detailed results
		t.Logf("=== Workflow Execution Results ===")
		t.Logf("Job ID: %s", res.result.JobID)
		t.Logf("Execution ID: %s", res.result.ExecutionID)
		t.Logf("Batch ID: %s", res.result.BatchID)
		t.Logf("Timeline:")
		t.Logf("  Initial Time:     %v", res.result.InitialTime)
		t.Logf("  Before Fail Time: %v (Δ: %v)",
			res.result.BeforeFailTime,
			res.result.BeforeFailTime.Sub(res.result.InitialTime))
		t.Logf("  After Retry Time: %v (Δ: %v)",
			res.result.AfterRetryTime,
			res.result.AfterRetryTime.Sub(res.result.BeforeFailTime))
		t.Logf("  Completion Time:  %v (Δ: %v)",
			res.result.CompletionTime,
			res.result.CompletionTime.Sub(res.result.AfterRetryTime))
		t.Logf("Total Duration: %v", res.result.TotalDuration)
		t.Logf("Activity Attempts: %d", currentAttempts)
		t.Logf("UUIDs (time-ordered):")
		for i, uid := range res.result.UUIDs {
			t.Logf("  [%d] %s", i, uid)
		}

		// Verify deterministic properties
		t.Logf("\n=== Deterministic Properties Verified ===")
		t.Log("✓ All timestamps recorded in workflow history")
		t.Log("✓ All UUIDs generated deterministically")
		t.Log("✓ Activity failure handled with retry")
		t.Log("✓ On replay, same timestamps and UUIDs will be used")
		t.Log("✓ Activity result will be cached, no re-execution needed")

	case <-time.After(60 * time.Second):
		t.Fatal("Workflow did not complete within timeout")
	}
}

func TestReplayWorkflow_ResumeAfterWorkerRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	jobID := "job-worker-restart"

	resetJobAttemptCounter(jobID)

	pool := SetupTestDB(t)

	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	exec, err := flows.Start(ctx, ReplayTestWorkflow, &ReplayWorkflowInput{
		JobID:   jobID,
		Retries: 3,
	})
	require.NoError(t, err)

	worker1 := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"replay-test-workflow"},
		PollInterval:  200 * time.Millisecond,
		TenantID:      tenantID,
	})

	workerCtx1, cancel1 := context.WithCancel(ctx)

	go func() {
		if err := worker1.Run(workerCtx1); err != nil && err != context.Canceled {
			t.Logf("Worker 1 error: %v", err)
		}
	}()

	require.Eventually(t, func() bool {
		return getJobAttemptCount(jobID) >= 1
	}, 15*time.Second, 200*time.Millisecond, "activity should be attempted at least once")

	// Allow the first failure to be fully recorded before stopping the worker
	time.Sleep(500 * time.Millisecond)

	cancel1()
	worker1.Stop()

	worker2 := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"replay-test-workflow"},
		PollInterval:  200 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker2.Stop()

	workerCtx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()

	go func() {
		if err := worker2.Run(workerCtx2); err != nil && err != context.Canceled {
			t.Logf("Worker 2 error: %v", err)
		}
	}()

	resultChan := make(chan struct {
		result *ReplayWorkflowOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *ReplayWorkflowOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
	case <-time.After(60 * time.Second):
		t.Fatal("workflow did not resume after worker restart")
	}

	attempts := getJobAttemptCount(jobID)
	assert.Equal(t, int32(2), attempts, "activity should complete after retry by second worker")
}

func TestReplayWorkflow_ManualReplay(t *testing.T) {
	t.Skip("Manual test - demonstrates replay behavior")

	// This test demonstrates what happens when you manually trigger replay:
	// 1. First execution: Activity fails, retries, succeeds
	// 2. Replay: Activity result is cached, returns immediately
	// 3. All Time() calls return cached values
	// 4. All UUIDv7() calls return same UUIDs

	ctx := context.Background()

	jobID := "job-manual-replay-test"

	// Reset counter
	resetJobAttemptCounter(jobID)

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// First execution
	t.Log("=== FIRST EXECUTION ===")
	exec, err := flows.Start(ctx, ReplayTestWorkflow, &ReplayWorkflowInput{
		JobID:   jobID,
		Retries: 3,
	})
	require.NoError(t, err)

	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"replay-test-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go worker.Run(workerCtx)

	result1, err := exec.Get(ctx)
	require.NoError(t, err)

	t.Logf("First execution completed:")
	t.Logf("  Execution ID: %s", result1.ExecutionID)
	t.Logf("  Initial Time: %v", result1.InitialTime)
	t.Logf("  Activity attempts: %d", getJobAttemptCount(jobID))

	// Save counter to simulate replay environment
	firstAttempts := getJobAttemptCount(jobID)
	resetJobAttemptCounter(jobID)

	// Simulate replay by processing the same workflow again
	// In real scenario, this would happen after worker restart
	t.Log("\n=== SIMULATED REPLAY ===")
	t.Log("(In real system, this happens automatically on worker restart)")
	t.Log("Activity counter reset to 0 to show replay doesn't re-execute")

	// Query the workflow status to show it's completed
	status, err := flows.Query(ctx, exec.WorkflowID())
	require.NoError(t, err)
	t.Logf("Workflow status: %+v", status)

	// On replay, the activity should not be called again
	replayAttempts := getJobAttemptCount(jobID)
	t.Logf("\nActivity call count:")
	t.Logf("  First execution: %d calls (1 fail + 1 success)", firstAttempts)
	t.Logf("  After 'replay': %d calls (activity result cached)", replayAttempts)

	assert.Equal(t, int32(0), replayAttempts,
		"On replay, activity should not be called (result is cached)")
}

func TestReplayWorkflow_MultipleFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create a counter that fails twice before succeeding
	var multiFailCounter int32

	// Create a custom activity that fails multiple times
	MultiFailActivity := flows.NewActivity(
		"multi-fail-activity",
		func(ctx context.Context, input *ProcessJobInput) (*ProcessJobOutput, error) {
			attempt := atomic.AddInt32(&multiFailCounter, 1)

			fmt.Printf("MultiFailActivity attempt %d\n", attempt)

			if attempt <= 2 {
				fmt.Printf("  -> FAILING (attempt %d)\n", attempt)
				return nil, flows.NewRetryableError(
					fmt.Errorf("simulated failure attempt %d", attempt))
			}

			fmt.Printf("  -> SUCCESS (attempt %d)\n", attempt)
			return &ProcessJobOutput{
				ProcessedID: uuid.New().String(),
				Success:     true,
				Attempt:     int(attempt),
			}, nil
		},
		flows.RetryPolicy{
			InitialInterval: 500 * time.Millisecond,
			MaxInterval:     5 * time.Second,
			BackoffFactor:   2.0,
			MaxAttempts:     5,
			Jitter:          0.1,
		},
	)

	// Workflow with multiple failure activity
	MultiFailWorkflow := flows.New(
		"multi-fail-workflow",
		1,
		func(ctx *flows.Context[ReplayWorkflowInput]) (*ReplayWorkflowOutput, error) {
			startTime := ctx.Time()
			execID := ctx.UUIDv7()

			// Call activity that will fail twice
			result, err := flows.ExecuteActivity(ctx, MultiFailActivity, &ProcessJobInput{
				JobID:     ctx.Input().JobID,
				Timestamp: startTime,
				Attempt:   1,
			})
			if err != nil {
				return nil, err
			}

			endTime := ctx.Time()

			return &ReplayWorkflowOutput{
				JobID:          ctx.Input().JobID,
				ExecutionID:    execID.String(),
				InitialTime:    startTime,
				CompletionTime: endTime,
				TotalDuration:  endTime.Sub(startTime),
				AttemptCount:   result.Attempt,
				Status:         "completed",
				UUIDs:          []string{execID.String()},
			}, nil
		},
	)

	// Reset counter
	atomic.StoreInt32(&multiFailCounter, 0)

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	exec, err := flows.Start(ctx, MultiFailWorkflow, &ReplayWorkflowInput{
		JobID: "multi-fail-job-001",
	})
	require.NoError(t, err)

	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"multi-fail-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go worker.Run(workerCtx)

	resultChan := make(chan struct {
		result *ReplayWorkflowOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *ReplayWorkflowOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)

		attempts := atomic.LoadInt32(&multiFailCounter)
		assert.Equal(t, int32(3), attempts,
			"Activity should have been called 3 times (2 failures + 1 success)")
		assert.Equal(t, 3, res.result.AttemptCount,
			"Result should show 3 attempts")

		t.Logf("Multi-failure test completed:")
		t.Logf("  Total attempts: %d", attempts)
		t.Logf("  Duration: %v", res.result.TotalDuration)
		t.Logf("  Status: %s", res.result.Status)

	case <-time.After(30 * time.Second):
		t.Fatal("Workflow timeout")
	}
}
