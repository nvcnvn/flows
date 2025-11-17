package examples_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type GetResultInput struct {
	Value string `json:"value"`
}

type GetResultOutput struct {
	Result string `json:"result"`
}

var GetResultActivity = flows.NewActivity(
	"getresult-activity",
	func(ctx context.Context, input *GetResultInput) (*GetResultOutput, error) {
		return &GetResultOutput{
			Result: "processed: " + input.Value,
		}, nil
	},
	flows.DefaultRetryPolicy,
)

// TestGetResult_WithoutExecution tests retrieving workflow results without keeping the Execution handle
func TestGetResult_WithoutExecution(t *testing.T) {
	t.Parallel()

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx := flows.WithTenantID(context.Background(), tenantID)

	// Define workflow for this test
	getResultWorkflow := flows.New(
		"getresult-workflow",
		1,
		func(ctx *flows.Context[GetResultInput]) (*GetResultOutput, error) {
			result, err := flows.ExecuteActivity(ctx, GetResultActivity, &GetResultInput{
				Value: ctx.Input().Value,
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	)

	// Start a workflow
	exec, err := flows.Start(ctx, getResultWorkflow, &GetResultInput{
		Value: "test-data-for-getresult",
	})
	require.NoError(t, err)
	require.NotNil(t, exec)

	// Save the workflow name and ID (simulating passing them between processes/services)
	workflowName := exec.WorkflowName()
	workflowID := exec.WorkflowID()

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"getresult-workflow"},
		Concurrency:   2,
		PollInterval:  500 * time.Millisecond,
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for workflow to complete using the new GetResult function
	// Simulate checking from a different service that doesn't have the Execution handle
	var result *GetResultOutput
	maxRetries := 20
	for i := 0; i < maxRetries; i++ {
		result, err = flows.GetResult[GetResultOutput](ctx, workflowName, workflowID)
		if err == nil {
			break
		}
		// If workflow is still running, wait and retry
		if err.Error() == "workflow is still running" || err.Error() == "workflow is pending execution" {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		// Other errors should fail immediately
		require.NoError(t, err, "Failed to get workflow result")
	}

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "processed: test-data-for-getresult", result.Result)
}

// TestWaitForResult tests the blocking WaitForResult function
func TestWaitForResult(t *testing.T) {
	t.Parallel()

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx := flows.WithTenantID(context.Background(), tenantID)

	// Define workflow for this test
	waitResultWorkflow := flows.New(
		"waitresult-workflow",
		1,
		func(ctx *flows.Context[GetResultInput]) (*GetResultOutput, error) {
			result, err := flows.ExecuteActivity(ctx, GetResultActivity, &GetResultInput{
				Value: ctx.Input().Value,
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	)

	// Start a workflow
	exec, err := flows.Start(ctx, waitResultWorkflow, &GetResultInput{
		Value: "test-data-for-waitresult",
	})
	require.NoError(t, err)
	require.NotNil(t, exec)

	// Save the workflow name and ID
	workflowName := exec.WorkflowName()
	workflowID := exec.WorkflowID()

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"waitresult-workflow"},
		Concurrency:   2,
		PollInterval:  500 * time.Millisecond,
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for result using WaitForResult (blocking call)
	resultChan := make(chan struct {
		result *GetResultOutput
		err    error
	})

	go func() {
		result, err := flows.WaitForResult[GetResultOutput](ctx, workflowName, workflowID)
		resultChan <- struct {
			result *GetResultOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.Equal(t, "processed: test-data-for-waitresult", res.result.Result)
	case <-time.After(15 * time.Second):
		t.Fatal("WaitForResult did not complete within timeout")
	}
}

// TestQueryWithOutput tests that Query now returns the output in WorkflowInfo
func TestQueryWithOutput(t *testing.T) {
	t.Parallel()

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx := flows.WithTenantID(context.Background(), tenantID)

	// Define workflow for this test
	queryWorkflow := flows.New(
		"query-workflow",
		1,
		func(ctx *flows.Context[GetResultInput]) (*GetResultOutput, error) {
			result, err := flows.ExecuteActivity(ctx, GetResultActivity, &GetResultInput{
				Value: ctx.Input().Value,
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	)

	// Start a workflow
	exec, err := flows.Start(ctx, queryWorkflow, &GetResultInput{
		Value: "test-query-output",
	})
	require.NoError(t, err)
	require.NotNil(t, exec)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"query-workflow"},
		Concurrency:   2,
		PollInterval:  500 * time.Millisecond,
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for completion using exec.Get first
	result, err := exec.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Now use Query to get the info with output
	info, err := flows.Query(ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, flows.StatusCompleted, info.Status)
	assert.NotEmpty(t, info.Output)

	// Manually deserialize the output from WorkflowInfo
	var output GetResultOutput
	err = json.Unmarshal(info.Output, &output)
	require.NoError(t, err)
	assert.Equal(t, "processed: test-query-output", output.Result)
}

// TestGetResult_NotFound tests error handling when workflow doesn't exist
func TestGetResult_NotFound(t *testing.T) {
	t.Parallel()

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx := flows.WithTenantID(context.Background(), tenantID)

	// Try to get result for non-existent workflow
	_, err := flows.GetResult[GetResultOutput](ctx, "getresult-workflow-shard-0", uuid.New())
	require.Error(t, err)
}

// TestGetResult_Failed tests getting result of a failed workflow
func TestGetResult_Failed(t *testing.T) {
	t.Parallel()

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx := flows.WithTenantID(context.Background(), tenantID)

	// Define a workflow that always fails
	failingWorkflow := flows.New("failing-getresult-workflow", 1, func(c *flows.Context[flows.NoInput]) (*flows.NoOutput, error) {
		return nil, fmt.Errorf("intentional failure")
	})

	// Use a workflow that will fail
	exec, err := flows.Start(ctx, failingWorkflow, &flows.NoInput{})
	require.NoError(t, err)
	require.NotNil(t, exec)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"failing-getresult-workflow"},
		Concurrency:   2,
		PollInterval:  500 * time.Millisecond,
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait a bit for workflow to fail
	time.Sleep(2 * time.Second)

	// Try to get result - should get an error
	_, err = flows.GetResult[flows.NoOutput](ctx, exec.WorkflowName(), exec.WorkflowID())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow failed")
}
