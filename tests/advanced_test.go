package examples_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/nvcnvn/flows"
)

type AdvancedRetryWorkflowInput struct {
	TestID string
}

type AdvancedRetryActivityInput struct {
	TestID string
}

var (
	advancedRetryCounters   = make(map[string]*atomic.Int32)
	advancedRetryCountersMu sync.Mutex
)

func getAdvancedRetryCounter(testID string) *atomic.Int32 {
	advancedRetryCountersMu.Lock()
	defer advancedRetryCountersMu.Unlock()

	if counter, exists := advancedRetryCounters[testID]; exists {
		return counter
	}

	counter := atomic.NewInt32(0)
	advancedRetryCounters[testID] = counter
	return counter
}

func resetAdvancedRetryCounter(testID string) {
	advancedRetryCountersMu.Lock()
	defer advancedRetryCountersMu.Unlock()

	delete(advancedRetryCounters, testID)
}

var AdvancedTestFailThenSucceedActivity = flows.NewActivity(
	"advanced-fail-then-succeed",
	func(ctx context.Context, input *AdvancedRetryActivityInput) (*flows.NoOutput, error) {
		fmt.Println("executing advanced-fail-then-succeed activity")
		counter := getAdvancedRetryCounter(input.TestID)
		attempt := counter.Add(1)
		if attempt <= 2 {
			return nil, flows.NewRetryableError(fmt.Errorf("transient failure"))
		}
		return &flows.NoOutput{}, nil
	},
	flows.RetryPolicy{
		MaxAttempts:     3,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     1 * time.Second,
		BackoffFactor:   2.0,
	},
)

var AdvancedTestRetryWorkflow = flows.New(
	"advanced-retry-workflow",
	1,
	func(ctx *flows.Context[AdvancedRetryWorkflowInput]) (*flows.NoOutput, error) {
		input := ctx.Input()
		_, err := flows.ExecuteActivity(ctx, AdvancedTestFailThenSucceedActivity, &AdvancedRetryActivityInput{TestID: input.TestID})
		if err != nil {
			return nil, err
		}
		return &flows.NoOutput{}, nil
	},
)

var AdvancedTestLogActivity = flows.NewActivity(
	"advanced-log-activity",
	func(ctx context.Context, input *string) (*flows.NoOutput, error) {
		fmt.Println(*input)
		return &flows.NoOutput{}, nil
	},
	flows.RetryPolicy{MaxAttempts: 1},
)

var AdvancedTestLoopWorkflow = flows.New(
	"advanced-loop-workflow",
	1,
	func(ctx *flows.Context[int]) (*int, error) {
		loopCount := ctx.Input()
		for i := 0; i < *loopCount; i++ {
			msg := fmt.Sprintf("Executing loop %d", i)
			_, err := flows.ExecuteActivity(ctx, AdvancedTestLogActivity, &msg)
			if err != nil {
				return nil, err
			}
		}
		return loopCount, nil
	},
)

var FailingActivity = flows.NewActivity(
	"failing-activity",
	func(ctx context.Context, input *flows.NoInput) (*flows.NoOutput, error) {
		return nil, flows.NewTerminalError(fmt.Errorf("i am a failure"))
	},
	flows.RetryPolicy{MaxAttempts: 1},
)

var FailingWorkflow = flows.New(
	"failing-workflow",
	1,
	func(ctx *flows.Context[flows.NoInput]) (*flows.NoOutput, error) {
		_, err := flows.ExecuteActivity(ctx, FailingActivity, &flows.NoInput{})
		return nil, err
	},
)

func TestAdvancedScenarios(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := SetupTestDB(t)
	engine := flows.NewEngine(db)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	worker := flows.NewWorker(db, flows.WorkerConfig{
		WorkflowNames: []string{
			"advanced-retry-workflow",
			"advanced-loop-workflow",
			"failing-workflow",
		},
	})

	go func() {
		err := worker.Run(ctx)
		require.NoError(t, err)
	}()
	defer worker.Stop()

	t.Run("TestActivityRetry", func(t *testing.T) {
		testID := uuid.New().String()
		resetAdvancedRetryCounter(testID)
		t.Cleanup(func() {
			resetAdvancedRetryCounter(testID)
		})

		exec, err := flows.Start(ctx, AdvancedTestRetryWorkflow, &AdvancedRetryWorkflowInput{TestID: testID})
		require.NoError(t, err)
		_, err = exec.Get(ctx)
		require.NoError(t, err)
		require.Equal(t, int32(3), getAdvancedRetryCounter(testID).Load())
	})

	t.Run("TestLoopWorkflow", func(t *testing.T) {
		loopCount := 5
		exec, err := flows.Start(ctx, AdvancedTestLoopWorkflow, &loopCount)
		require.NoError(t, err)
		result, err := exec.Get(ctx)
		require.NoError(t, err)
		require.Equal(t, 5, *result)
	})

	t.Run("TestWorkflowFail", func(t *testing.T) {
		exec, err := flows.Start(ctx, FailingWorkflow, &flows.NoInput{})
		require.NoError(t, err)

		_, err = exec.Get(ctx)
		require.Error(t, err)

		status, err := flows.Query(ctx, exec.WorkflowName(), exec.WorkflowID())
		require.NoError(t, err)
		require.Equal(t, flows.StatusFailed, status.Status)
	})
}
