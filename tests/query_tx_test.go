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

type QueryTxInput struct {
	Data string
}

type QueryTxOutput struct {
	Result string
}

// TestQueryWithTransaction tests that Query can be used within a transaction
func TestQueryWithTransaction(t *testing.T) {
	t.Parallel()

	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)

	ctx := context.Background()
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Define a simple workflow
	workflow := flows.New(
		"query-tx-test",
		1,
		func(ctx *flows.Context[QueryTxInput]) (*QueryTxOutput, error) {
			return &QueryTxOutput{
				Result: "processed: " + ctx.Input().Data,
			}, nil
		},
	)

	// Start workflow
	exec, err := flows.StartWith(engine, ctx, workflow, &QueryTxInput{Data: "test-data"})
	require.NoError(t, err)

	// Start worker to process the workflow
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		WorkflowNames: []string{"query-tx-test"},
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

	// Wait for completion
	result, err := flows.WaitForResultWith[QueryTxOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, "processed: test-data", result.Result)

	// Test 1: Query within a transaction (read-only)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	//nolint:errcheck
	defer tx.Rollback(ctx)

	// Query workflow within transaction
	info, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID(), flows.WithQueryTx(tx))
	require.NoError(t, err)
	assert.Equal(t, flows.StatusCompleted, info.Status)

	// Test 2: GetResult within a transaction
	resultTx, err := flows.GetResultWith[QueryTxOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID(), flows.WithQueryTx(tx))
	require.NoError(t, err)
	assert.Equal(t, "processed: test-data", resultTx.Result)

	// Commit the transaction
	err = tx.Commit(ctx)
	require.NoError(t, err)
}
