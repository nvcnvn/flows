package examples_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SimpleInput for testing multi-tenancy
type SimpleInput struct {
	Value string `json:"value"`
}

type SimpleOutput struct {
	Result string `json:"result"`
}

// SimpleActivity for testing
var SimpleActivity = flows.NewActivity(
	"simple-activity",
	func(ctx context.Context, input *SimpleInput) (*SimpleOutput, error) {
		return &SimpleOutput{
			Result: "processed: " + input.Value,
		}, nil
	},
	flows.DefaultRetryPolicy,
)

// SimpleWorkflow for testing multi-tenancy
var SimpleWorkflow = flows.New(
	"simple-workflow",
	1,
	func(ctx *flows.Context[SimpleInput]) (*SimpleOutput, error) {
		input := ctx.Input()
		result, err := flows.ExecuteActivity(ctx, SimpleActivity, input)
		if err != nil {
			return nil, err
		}
		return result, nil
	},
)

type TenantSignalInput struct {
	TenantAlias string `json:"tenant_alias"`
}

type TenantSignalPayload struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Message  string    `json:"message"`
}

type TenantSignalOutput struct {
	Summary string    `json:"summary"`
	Tenant  uuid.UUID `json:"tenant"`
}

var TenantSignalWorkflow = flows.New(
	"tenant-signal-workflow",
	1,
	func(ctx *flows.Context[TenantSignalInput]) (*TenantSignalOutput, error) {
		// Run a simple activity first to mimic multi-step workflows
		processed, err := flows.ExecuteActivity(ctx, SimpleActivity, &SimpleInput{Value: ctx.Input().TenantAlias})
		if err != nil {
			return nil, err
		}

		// Wait for a tenant-specific signal
		payload, err := flows.WaitForSignal[TenantSignalInput, TenantSignalPayload](ctx, "tenant-ready")
		if err != nil {
			return nil, err
		}

		summary := fmt.Sprintf("%s | signal=%s", processed.Result, payload.Message)
		return &TenantSignalOutput{
			Summary: summary,
			Tenant:  payload.TenantID,
		}, nil
	},
)

func TestMultiTenantIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup database connection
	pool := SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Create two different tenants
	tenant1 := uuid.New()
	tenant2 := uuid.New()

	t.Logf("Tenant 1: %s", tenant1)
	t.Logf("Tenant 2: %s", tenant2)

	// Start workflow for tenant 1
	ctx1 := flows.WithTenantID(ctx, tenant1)
	exec1, err := flows.Start(ctx1, SimpleWorkflow, &SimpleInput{Value: "tenant1-data"})
	require.NoError(t, err)

	// Start workflow for tenant 2
	ctx2 := flows.WithTenantID(ctx, tenant2)
	exec2, err := flows.Start(ctx2, SimpleWorkflow, &SimpleInput{Value: "tenant2-data"})
	require.NoError(t, err)

	t.Logf("Tenant 1 workflow: %s", exec1.WorkflowID())
	t.Logf("Tenant 2 workflow: %s", exec2.WorkflowID())

	// Start worker that processes workflows for all tenants
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   4,
		WorkflowNames: []string{"simple-workflow"},
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

	// Get results with timeout
	resultChan1 := make(chan struct {
		result *SimpleOutput
		err    error
	})
	resultChan2 := make(chan struct {
		result *SimpleOutput
		err    error
	})

	go func() {
		result, err := exec1.Get(ctx1)
		resultChan1 <- struct {
			result *SimpleOutput
			err    error
		}{result, err}
	}()

	go func() {
		result, err := exec2.Get(ctx2)
		resultChan2 <- struct {
			result *SimpleOutput
			err    error
		}{result, err}
	}()

	// Verify tenant 1 result
	select {
	case res := <-resultChan1:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.Equal(t, "processed: tenant1-data", res.result.Result)
		t.Logf("Tenant 1 completed: %+v", res.result)
	case <-time.After(15 * time.Second):
		t.Fatal("Tenant 1 workflow did not complete within timeout")
	}

	// Verify tenant 2 result
	select {
	case res := <-resultChan2:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.Equal(t, "processed: tenant2-data", res.result.Result)
		t.Logf("Tenant 2 completed: %+v", res.result)
	case <-time.After(15 * time.Second):
		t.Fatal("Tenant 2 workflow did not complete within timeout")
	}

	// Verify tenant isolation - tenant 1 should not see tenant 2's workflow
	status, err := flows.Query(ctx1, exec2.WorkflowName(), exec2.WorkflowID())
	assert.Error(t, err, "Should not be able to query other tenant's workflow")
	assert.Nil(t, status, "Status should be nil for cross-tenant query")
}

func TestMultiTenantWorkerIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup database connection
	pool := SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Create two tenants
	tenant1 := uuid.New()
	tenant2 := uuid.New()

	// Start workflows for both tenants
	ctx1 := flows.WithTenantID(ctx, tenant1)
	exec1, err := flows.Start(ctx1, SimpleWorkflow, &SimpleInput{Value: "tenant1"})
	require.NoError(t, err)

	ctx2 := flows.WithTenantID(ctx, tenant2)
	exec2, err := flows.Start(ctx2, SimpleWorkflow, &SimpleInput{Value: "tenant2"})
	require.NoError(t, err)

	// Start a single worker - it processes workflows from all tenants
	// This simulates production where workers are not tenant-specific
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"simple-workflow"},
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

	// Both tenants should complete with single worker
	resultChan1 := make(chan struct {
		result *SimpleOutput
		err    error
	})
	resultChan2 := make(chan struct {
		result *SimpleOutput
		err    error
	})

	go func() {
		result, err := exec1.Get(ctx1)
		resultChan1 <- struct {
			result *SimpleOutput
			err    error
		}{result, err}
	}()

	go func() {
		result, err := exec2.Get(ctx2)
		resultChan2 <- struct {
			result *SimpleOutput
			err    error
		}{result, err}
	}()

	// Wait for both tenants to complete
	select {
	case res := <-resultChan1:
		require.NoError(t, res.err)
		assert.Equal(t, "processed: tenant1", res.result.Result)
		t.Log("Tenant 1 completed")
	case <-time.After(15 * time.Second):
		t.Fatal("Tenant 1 should have completed")
	}

	select {
	case res := <-resultChan2:
		require.NoError(t, res.err)
		assert.Equal(t, "processed: tenant2", res.result.Result)
		t.Log("Tenant 2 completed")
	case <-time.After(15 * time.Second):
		t.Fatal("Tenant 2 should have completed")
	}
}

func TestMultiTenantSignalIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	pool := SetupTestDB(t)

	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenant1 := uuid.New()
	tenant2 := uuid.New()

	ctx1 := flows.WithTenantID(ctx, tenant1)
	ctx2 := flows.WithTenantID(ctx, tenant2)

	exec1, err := flows.Start(ctx1, TenantSignalWorkflow, &TenantSignalInput{TenantAlias: "tenant1"})
	require.NoError(t, err)

	exec2, err := flows.Start(ctx2, TenantSignalWorkflow, &TenantSignalInput{TenantAlias: "tenant2"})
	require.NoError(t, err)

	// Single worker processes workflows from all tenants
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   4,
		WorkflowNames: []string{"tenant-signal-workflow"},
		PollInterval:  200 * time.Millisecond,
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

	require.Eventually(t, func() bool {
		status1, err := flows.Query(ctx1, exec1.WorkflowName(), exec1.WorkflowID())
		if err != nil {
			return false
		}
		status2, err := flows.Query(ctx2, exec2.WorkflowName(), exec2.WorkflowID())
		if err != nil {
			return false
		}
		return status1.Status == flows.StatusRunning && status2.Status == flows.StatusRunning
	}, 10*time.Second, 200*time.Millisecond, "workflows should be running before signaling")

	err = flows.SendSignal(ctx1, exec2.WorkflowName(), exec2.WorkflowID(), "tenant-ready", &TenantSignalPayload{
		TenantID: tenant1,
		Message:  "wrong-tenant",
	})
	require.Error(t, err, "cross-tenant signal should be rejected")
	assert.Contains(t, err.Error(), "failed to get workflow")

	err = flows.SendSignal(ctx1, exec1.WorkflowName(), exec1.WorkflowID(), "tenant-ready", &TenantSignalPayload{
		TenantID: tenant1,
		Message:  "ack-tenant1",
	})
	require.NoError(t, err)

	err = flows.SendSignal(ctx2, exec2.WorkflowName(), exec2.WorkflowID(), "tenant-ready", &TenantSignalPayload{
		TenantID: tenant2,
		Message:  "ack-tenant2",
	})
	require.NoError(t, err)

	resultChan1 := make(chan struct {
		result *TenantSignalOutput
		err    error
	})
	resultChan2 := make(chan struct {
		result *TenantSignalOutput
		err    error
	})

	go func() {
		result, err := exec1.Get(ctx1)
		resultChan1 <- struct {
			result *TenantSignalOutput
			err    error
		}{result, err}
	}()

	go func() {
		result, err := exec2.Get(ctx2)
		resultChan2 <- struct {
			result *TenantSignalOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan1:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.Contains(t, res.result.Summary, "processed: tenant1")
		assert.Contains(t, res.result.Summary, "signal=ack-tenant1")
		assert.Equal(t, tenant1, res.result.Tenant)
	case <-time.After(20 * time.Second):
		t.Fatal("tenant 1 workflow did not complete in time")
	}

	select {
	case res := <-resultChan2:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.Contains(t, res.result.Summary, "processed: tenant2")
		assert.Contains(t, res.result.Summary, "signal=ack-tenant2")
		assert.Equal(t, tenant2, res.result.Tenant)
	case <-time.After(20 * time.Second):
		t.Fatal("tenant 2 workflow did not complete in time")
	}

	status1, err := flows.Query(ctx1, exec1.WorkflowName(), exec1.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, flows.StatusCompleted, status1.Status)

	status2, err := flows.Query(ctx2, exec2.WorkflowName(), exec2.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, flows.StatusCompleted, status2.Status)
}
