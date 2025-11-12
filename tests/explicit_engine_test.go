package examples_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test data structures
type ExplicitEngineInput struct {
	Data string
}

type ExplicitEngineOutput struct {
	Message string
}

// TestExplicitEngine_BasicWorkflow tests using the explicit engine pattern (Pattern 2)
// This pattern passes the engine instance directly instead of using global state
func TestExplicitEngine_BasicWorkflow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)

	// Pattern 2: Create engine but DON'T set it globally
	engine := flows.NewEngine(pool)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Simple workflow
	workflow := flows.New(
		"explicit-engine-workflow",
		1,
		func(ctx *flows.Context[ExplicitEngineInput]) (*ExplicitEngineOutput, error) {
			return &ExplicitEngineOutput{
				Message: "Processed: " + ctx.Input().Data,
			}, nil
		},
	)

	// Use StartWith instead of Start - passing engine explicitly
	exec, err := flows.StartWith(engine, ctx, workflow, &ExplicitEngineInput{
		Data: "test-data",
	})
	require.NoError(t, err)
	require.NotNil(t, exec)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"explicit-engine-workflow"},
		Concurrency:   1,
		PollInterval:  100 * time.Millisecond,
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

	// Use WaitForResultWith instead of WaitForResult - passing engine explicitly
	result, err := flows.WaitForResultWith[ExplicitEngineOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Processed: test-data", result.Message)
}

// TestExplicitEngine_QueryAndGetResult tests query and result operations with explicit engine
func TestExplicitEngine_QueryAndGetResult(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)

	// Pattern 2: Create engine explicitly
	engine := flows.NewEngine(pool)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	workflow := flows.New(
		"query-workflow-explicit",
		1,
		func(ctx *flows.Context[ExplicitEngineInput]) (*ExplicitEngineOutput, error) {
			// Do some work
			ctx.Sleep(1 * time.Second)
			return &ExplicitEngineOutput{
				Message: "Completed: " + ctx.Input().Data,
			}, nil
		},
	)

	// Start workflow with explicit engine
	exec, err := flows.StartWith(engine, ctx, workflow, &ExplicitEngineInput{
		Data: "query-test",
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"query-workflow-explicit"},
		Concurrency:   1,
		PollInterval:  100 * time.Millisecond,
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

	// Query status using explicit engine - should be running
	time.Sleep(500 * time.Millisecond)
	info, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, flows.StatusRunning, info.Status)

	// Wait for completion
	result, err := flows.WaitForResultWith[ExplicitEngineOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Completed: query-test", result.Message)

	// Query again - should be completed
	info, err = flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, flows.StatusCompleted, info.Status)

	// GetResult should also work
	result2, err := flows.GetResultWith[ExplicitEngineOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Completed: query-test", result2.Message)
}

// TestExplicitEngine_NoGlobalState verifies that explicit engine pattern doesn't require global state
func TestExplicitEngine_NoGlobalState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)

	// Pattern 2: Create engine WITHOUT setting it globally
	engine := flows.NewEngine(pool)
	// Note: We deliberately DON'T call flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	workflow := flows.New(
		"no-global-workflow",
		1,
		func(ctx *flows.Context[ExplicitEngineInput]) (*ExplicitEngineOutput, error) {
			return &ExplicitEngineOutput{
				Message: "Works without global state!",
			}, nil
		},
	)

	// This should work fine with explicit engine, even without global state
	exec, err := flows.StartWith(engine, ctx, workflow, &ExplicitEngineInput{
		Data: "no-global",
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"no-global-workflow"},
		Concurrency:   1,
		PollInterval:  100 * time.Millisecond,
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

	// All operations work with explicit engine
	result, err := flows.WaitForResultWith[ExplicitEngineOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Works without global state!", result.Message)
}

// TestExplicitEngine_CustomShardConfig tests using custom shard config with explicit engine
func TestExplicitEngine_CustomShardConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)

	// Create custom shard config with 5 shards instead of default 9
	shardConfig := flows.NewShardConfig(5)

	// Create engine with custom shard config
	engine := flows.NewEngine(pool, flows.WithShardConfig(shardConfig))

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	workflow := flows.New(
		"custom-shardconfig-workflow",
		1,
		func(ctx *flows.Context[ExplicitEngineInput]) (*ExplicitEngineOutput, error) {
			return &ExplicitEngineOutput{
				Message: "Custom shard config: " + ctx.Input().Data,
			}, nil
		},
	)

	// Start workflow - should use custom shard config for sharding
	exec, err := flows.StartWith(engine, ctx, workflow, &ExplicitEngineInput{
		Data: "test-shard",
	})
	require.NoError(t, err)

	// Worker should use same custom shard config
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"custom-shardconfig-workflow"},
		Concurrency:   1,
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
		Sharder:       shardConfig, // Use same sharder
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Verify workflow completes with custom sharding
	result, err := flows.WaitForResultWith[ExplicitEngineOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Custom shard config: test-shard", result.Message)

	// Verify shard config has 5 shards
	assert.Equal(t, 5, shardConfig.NumShards())
}

// TestExplicitEngine_MultipleEnginesWithDifferentShardConfigs tests multiple engines with different shard configs
func TestExplicitEngine_MultipleEnginesWithDifferentShardConfigs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)

	// Engine 1: Default shard config (9 shards)
	engine1 := flows.NewEngine(pool)

	// Engine 2: Custom shard config (2 shards)
	shardConfig2 := flows.NewShardConfig(2)
	engine2 := flows.NewEngine(pool, flows.WithShardConfig(shardConfig2))

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	workflow := flows.New(
		"multi-engine-workflow",
		1,
		func(ctx *flows.Context[ExplicitEngineInput]) (*ExplicitEngineOutput, error) {
			return &ExplicitEngineOutput{
				Message: "Result: " + ctx.Input().Data,
			}, nil
		},
	)

	// Start workflow with engine 1 (default 9 shards)
	exec1, err := flows.StartWith(engine1, ctx, workflow, &ExplicitEngineInput{
		Data: "engine1",
	})
	require.NoError(t, err)

	// Start workflow with engine 2 (custom 2 shards)
	exec2, err := flows.StartWith(engine2, ctx, workflow, &ExplicitEngineInput{
		Data: "engine2",
	})
	require.NoError(t, err)

	// Worker for engine 1's shard config (9 shards)
	worker1 := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"multi-engine-workflow"},
		Concurrency:   1,
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
		// Uses default global shard config (9 shards)
	})
	defer worker1.Stop()

	// Worker for engine 2's shard config (2 shards)
	worker2 := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"multi-engine-workflow"},
		Concurrency:   1,
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
		Sharder:       shardConfig2,
	})
	defer worker2.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := worker1.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker1 error: %v", err)
		}
	}()

	go func() {
		if err := worker2.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker2 error: %v", err)
		}
	}()

	// Both workflows should complete
	result1, err := flows.WaitForResultWith[ExplicitEngineOutput](engine1, ctx, exec1.WorkflowName(), exec1.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Result: engine1", result1.Message)

	result2, err := flows.WaitForResultWith[ExplicitEngineOutput](engine2, ctx, exec2.WorkflowName(), exec2.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "Result: engine2", result2.Message)
}
