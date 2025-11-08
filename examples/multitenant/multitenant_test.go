package multitenant

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/nvcnvn/flows/testutil"
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

func TestMultiTenantIsolation(t *testing.T) {
	ctx := context.Background()

	// Setup database connection
	pool := testutil.SetupTestDB(t)

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

	// Start workers for each tenant
	worker1 := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"simple-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenant1,
	})

	worker2 := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"simple-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenant2,
	})

	worker1Ctx, cancel1 := context.WithCancel(ctx)
	defer cancel1()
	worker2Ctx, cancel2 := context.WithCancel(ctx)
	defer cancel2()

	go func() {
		if err := worker1.Run(worker1Ctx); err != nil && err != context.Canceled {
			t.Logf("Worker 1 error: %v", err)
		}
	}()

	go func() {
		if err := worker2.Run(worker2Ctx); err != nil && err != context.Canceled {
			t.Logf("Worker 2 error: %v", err)
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
	status, err := flows.Query(ctx1, exec2.WorkflowID())
	assert.Error(t, err, "Should not be able to query other tenant's workflow")
	assert.Nil(t, status, "Status should be nil for cross-tenant query")

	// Cleanup
	worker1.Stop()
	worker2.Stop()
}

func TestMultiTenantWorkerIsolation(t *testing.T) {
	ctx := context.Background()

	// Setup database connection
	pool := testutil.SetupTestDB(t)

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

	// Start worker ONLY for tenant1
	worker1 := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"simple-workflow"},
		PollInterval:  500 * time.Millisecond,
		TenantID:      tenant1, // Only processes tenant1
	})

	worker1Ctx, cancel1 := context.WithCancel(ctx)
	defer cancel1()

	go func() {
		if err := worker1.Run(worker1Ctx); err != nil && err != context.Canceled {
			t.Logf("Worker 1 error: %v", err)
		}
	}()

	// Tenant 1 should complete
	resultChan1 := make(chan struct {
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

	select {
	case res := <-resultChan1:
		require.NoError(t, res.err)
		assert.Equal(t, "processed: tenant1", res.result.Result)
		t.Log("Tenant 1 completed as expected")
	case <-time.After(15 * time.Second):
		t.Fatal("Tenant 1 should have completed")
	}

	// Tenant 2 should NOT complete (no worker)
	resultChan2 := make(chan struct {
		result *SimpleOutput
		err    error
	})

	go func() {
		timeoutCtx, cancel := context.WithTimeout(ctx2, 5*time.Second)
		defer cancel()
		result, err := exec2.Get(timeoutCtx)
		resultChan2 <- struct {
			result *SimpleOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan2:
		// Should timeout or be in progress
		if res.err != nil {
			t.Logf("Tenant 2 correctly did not complete: %v", res.err)
		} else {
			t.Fatal("Tenant 2 should not have completed without dedicated worker")
		}
	case <-time.After(7 * time.Second):
		t.Log("Tenant 2 correctly timed out (no worker)")
	}

	worker1.Stop()
}
