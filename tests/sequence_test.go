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

// Package sequence_test validates the sequence number counting behavior.
//
// ## Why Sequence Numbers Matter
//
// Sequence numbers are critical for deterministic replay - they ensure that
// operations (activities, timers, signals) are replayed in the exact same order
// every time a workflow is resumed. Without proper sequence tracking:
// - Activities could be re-executed instead of using cached results
// - Workflows could get stuck in infinite loops
// - State could become inconsistent across pauses/resumes
//
// ## What These Tests Validate
//
// 1. **Single Activity** - Basic sequence increment (seq 0 → 1)
// 2. **Multiple Activities** - Consecutive sequence numbers (0 → 1 → 2 → 3...)
// 3. **Mixed Operations** - Sequence tracking across different operation types:
//    - Activities (ExecuteActivity)
//    - Timers (Sleep)
//    - Time recording (Time)
//    - UUID generation (UUIDv7)
// 4. **Replay Consistency** - Activities executed only once, cached on replay
// 5. **Loop Operations** - Sequence numbers in loops maintain correct order
// 6. **Conditional Branching** - Different code paths get correct sequence numbers
//
// ## Test Design Philosophy
//
// These tests use ONLY the public API (flows.New, flows.ExecuteActivity, etc.)
// without accessing internal implementation details. This allows the internal
// implementation to be refactored safely - as long as the public behavior is
// correct, these tests will pass.
//
// ## Protection Against Regressions
//
// The critical bug fixed in the type-erasure refactoring (Nov 2025) was that
// the sequence number wasn't being properly returned from the workflow execution
// function, causing workflows to repeat operations infinitely. These tests would
// have caught that bug immediately.

// Test inputs and outputs
type SequenceTestInput struct {
	OperationCount int `json:"operation_count"`
}

type SequenceTestOutput struct {
	OperationsCompleted int      `json:"operations_completed"`
	Summary             string   `json:"summary"`
	ActivityResults     []string `json:"activity_results"`
}

type SimpleActivityInput struct {
	Operation string `json:"operation"`
	Index     int    `json:"index"`
}

type SimpleActivityOutput struct {
	Result string `json:"result"`
}

// Simple activity that just returns a result
var SequenceActivity = flows.NewActivity(
	"sequence-activity",
	func(ctx context.Context, input *SimpleActivityInput) (*SimpleActivityOutput, error) {
		return &SimpleActivityOutput{
			Result: fmt.Sprintf("op_%s_%d", input.Operation, input.Index),
		}, nil
	},
	flows.DefaultRetryPolicy,
)

// TestSequenceNumber_SingleActivity validates that a single activity increments the sequence number
func TestSequenceNumber_SingleActivity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Workflow with a single activity
	singleActivityWorkflow := flows.New(
		"single-activity-workflow",
		1,
		func(ctx *flows.Context[SequenceTestInput]) (*SequenceTestOutput, error) {
			result, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
				Operation: "single",
				Index:     1,
			})
			if err != nil {
				return nil, err
			}

			return &SequenceTestOutput{
				OperationsCompleted: 1,
				Summary:             "single activity completed",
				ActivityResults:     []string{result.Result},
			}, nil
		},
	)

	// Start workflow
	exec, err := flows.Start(ctx, singleActivityWorkflow, &SequenceTestInput{
		OperationCount: 1,
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"single-activity-workflow"},
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = worker.Run(workerCtx)
	}()

	// Wait for completion
	result, err := exec.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 1, result.OperationsCompleted)
	assert.Len(t, result.ActivityResults, 1)
	assert.Equal(t, "op_single_1", result.ActivityResults[0])
}

// TestSequenceNumber_MultipleActivities validates consecutive sequence numbers for multiple activities
func TestSequenceNumber_MultipleActivities(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Workflow with multiple sequential activities
	multiActivityWorkflow := flows.New(
		"multi-activity-workflow",
		1,
		func(ctx *flows.Context[SequenceTestInput]) (*SequenceTestOutput, error) {
			count := ctx.Input().OperationCount
			results := make([]string, 0, count)

			// Execute multiple activities in sequence
			for i := 0; i < count; i++ {
				result, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
					Operation: "multi",
					Index:     i,
				})
				if err != nil {
					return nil, fmt.Errorf("activity %d failed: %w", i, err)
				}
				results = append(results, result.Result)
			}

			return &SequenceTestOutput{
				OperationsCompleted: count,
				Summary:             fmt.Sprintf("completed %d activities", count),
				ActivityResults:     results,
			}, nil
		},
	)

	// Start workflow with 5 activities
	exec, err := flows.Start(ctx, multiActivityWorkflow, &SequenceTestInput{
		OperationCount: 5,
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"multi-activity-workflow"},
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = worker.Run(workerCtx)
	}()

	// Wait for completion
	result, err := exec.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all activities completed in order
	assert.Equal(t, 5, result.OperationsCompleted)
	assert.Len(t, result.ActivityResults, 5)
	for i := 0; i < 5; i++ {
		assert.Equal(t, fmt.Sprintf("op_multi_%d", i), result.ActivityResults[i],
			"Activity %d should have correct result", i)
	}
}

// TestSequenceNumber_MixedOperations validates sequence numbers across different operation types
func TestSequenceNumber_MixedOperations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	type MixedOutput struct {
		ActivityResults []string  `json:"activity_results"`
		TimeRecorded    time.Time `json:"time_recorded"`
		UUIDGenerated   string    `json:"uuid_generated"`
	}

	// Workflow with mixed operations: activity, time, UUID, sleep, activity
	mixedWorkflow := flows.New(
		"mixed-operations-workflow",
		1,
		func(ctx *flows.Context[SequenceTestInput]) (*MixedOutput, error) {
			results := make([]string, 0)

			// Operation 1: Activity
			result1, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
				Operation: "first",
				Index:     1,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result1.Result)

			// Operation 2: Time (deterministic)
			recordedTime := ctx.Time()

			// Operation 3: UUID generation (deterministic)
			generatedUUID := ctx.UUIDv7()

			// Operation 4: Sleep (timer)
			if err := ctx.Sleep(100 * time.Millisecond); err != nil {
				return nil, err
			}

			// Operation 5: Another activity
			result2, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
				Operation: "second",
				Index:     2,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result2.Result)

			return &MixedOutput{
				ActivityResults: results,
				TimeRecorded:    recordedTime,
				UUIDGenerated:   generatedUUID.String(),
			}, nil
		},
	)

	// Start workflow
	exec, err := flows.Start(ctx, mixedWorkflow, &SequenceTestInput{
		OperationCount: 5, // 2 activities + time + uuid + sleep
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"mixed-operations-workflow"},
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = worker.Run(workerCtx)
	}()

	// Wait for completion
	result, err := exec.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all operations completed
	assert.Len(t, result.ActivityResults, 2)
	assert.Equal(t, "op_first_1", result.ActivityResults[0])
	assert.Equal(t, "op_second_2", result.ActivityResults[1])
	assert.False(t, result.TimeRecorded.IsZero(), "Time should be recorded")
	assert.NotEmpty(t, result.UUIDGenerated, "UUID should be generated")
}

// TestSequenceNumber_ReplayConsistency validates that sequence numbers are consistent on replay
func TestSequenceNumber_ReplayConsistency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Activity that tracks how many times it's actually executed
	executionCount := make(map[string]int)
	replayActivity := flows.NewActivity(
		"replay-tracking-activity",
		func(ctx context.Context, input *SimpleActivityInput) (*SimpleActivityOutput, error) {
			key := fmt.Sprintf("%s_%d", input.Operation, input.Index)
			executionCount[key]++
			return &SimpleActivityOutput{
				Result: fmt.Sprintf("executed_%s", key),
			}, nil
		},
		flows.DefaultRetryPolicy,
	)

	// Workflow that can be interrupted and resumed
	type ReplayInput struct {
		ShouldPause bool `json:"should_pause"`
	}

	type ReplayOutput struct {
		Results   []string `json:"results"`
		Completed bool     `json:"completed"`
	}

	replayWorkflow := flows.New(
		"replay-consistency-workflow",
		1,
		func(ctx *flows.Context[ReplayInput]) (*ReplayOutput, error) {
			results := make([]string, 0)

			// Activity 1
			result1, err := flows.ExecuteActivity(ctx, replayActivity, &SimpleActivityInput{
				Operation: "replay",
				Index:     1,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result1.Result)

			// Activity 2
			result2, err := flows.ExecuteActivity(ctx, replayActivity, &SimpleActivityInput{
				Operation: "replay",
				Index:     2,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result2.Result)

			// Activity 3
			result3, err := flows.ExecuteActivity(ctx, replayActivity, &SimpleActivityInput{
				Operation: "replay",
				Index:     3,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result3.Result)

			return &ReplayOutput{
				Results:   results,
				Completed: true,
			}, nil
		},
	)

	// Start workflow
	exec, err := flows.Start(ctx, replayWorkflow, &ReplayInput{
		ShouldPause: false,
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"replay-consistency-workflow"},
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = worker.Run(workerCtx)
	}()

	// Wait for completion
	result, err := exec.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all activities completed
	assert.True(t, result.Completed)
	assert.Len(t, result.Results, 3)
	assert.Equal(t, "executed_replay_1", result.Results[0])
	assert.Equal(t, "executed_replay_2", result.Results[1])
	assert.Equal(t, "executed_replay_3", result.Results[2])

	// Critical assertion: Each activity should only be executed ONCE
	// If sequence numbers are working correctly, replay will use cached results
	// and not re-execute activities
	assert.Equal(t, 1, executionCount["replay_1"],
		"Activity 1 should only execute once (not on every replay)")
	assert.Equal(t, 1, executionCount["replay_2"],
		"Activity 2 should only execute once (not on every replay)")
	assert.Equal(t, 1, executionCount["replay_3"],
		"Activity 3 should only execute once (not on every replay)")
}

// TestSequenceNumber_LoopOperations validates sequence numbers in loops
func TestSequenceNumber_LoopOperations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Workflow with a loop of activities
	loopWorkflow := flows.New(
		"loop-operations-workflow",
		1,
		func(ctx *flows.Context[SequenceTestInput]) (*SequenceTestOutput, error) {
			count := ctx.Input().OperationCount
			results := make([]string, 0, count)

			// Loop with multiple activities per iteration
			for i := 0; i < count; i++ {
				// Two activities per loop iteration
				result1, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
					Operation: "loop_a",
					Index:     i,
				})
				if err != nil {
					return nil, err
				}
				results = append(results, result1.Result)

				result2, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
					Operation: "loop_b",
					Index:     i,
				})
				if err != nil {
					return nil, err
				}
				results = append(results, result2.Result)
			}

			return &SequenceTestOutput{
				OperationsCompleted: count * 2,
				Summary:             fmt.Sprintf("completed %d loop iterations with 2 activities each", count),
				ActivityResults:     results,
			}, nil
		},
	)

	// Start workflow with 3 loop iterations (6 total activities)
	exec, err := flows.Start(ctx, loopWorkflow, &SequenceTestInput{
		OperationCount: 3,
	})
	require.NoError(t, err)

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   2,
		WorkflowNames: []string{"loop-operations-workflow"},
		PollInterval:  100 * time.Millisecond,
		TenantID:      tenantID,
	})
	defer worker.Stop()

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = worker.Run(workerCtx)
	}()

	// Wait for completion
	result, err := exec.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all activities completed in correct order
	assert.Equal(t, 6, result.OperationsCompleted)
	assert.Len(t, result.ActivityResults, 6)

	expectedResults := []string{
		"op_loop_a_0", "op_loop_b_0", // iteration 0
		"op_loop_a_1", "op_loop_b_1", // iteration 1
		"op_loop_a_2", "op_loop_b_2", // iteration 2
	}

	for i, expected := range expectedResults {
		assert.Equal(t, expected, result.ActivityResults[i],
			"Activity %d should have correct result", i)
	}
}

// TestSequenceNumber_ConditionalBranching validates sequence numbers with conditional logic
func TestSequenceNumber_ConditionalBranching(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := SetupTestDB(t)
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	type BranchInput struct {
		TakeBranchA bool `json:"take_branch_a"`
	}

	type BranchOutput struct {
		BranchTaken     string   `json:"branch_taken"`
		ActivityResults []string `json:"activity_results"`
	}

	// Workflow with conditional branching
	branchWorkflow := flows.New(
		"conditional-branch-workflow",
		1,
		func(ctx *flows.Context[BranchInput]) (*BranchOutput, error) {
			results := make([]string, 0)

			// Common activity before branch
			result1, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
				Operation: "before_branch",
				Index:     1,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result1.Result)

			var branchTaken string
			if ctx.Input().TakeBranchA {
				// Branch A: 2 activities
				branchTaken = "branch_a"
				result2, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
					Operation: "branch_a",
					Index:     1,
				})
				if err != nil {
					return nil, err
				}
				results = append(results, result2.Result)

				result3, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
					Operation: "branch_a",
					Index:     2,
				})
				if err != nil {
					return nil, err
				}
				results = append(results, result3.Result)
			} else {
				// Branch B: 1 activity
				branchTaken = "branch_b"
				result2, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
					Operation: "branch_b",
					Index:     1,
				})
				if err != nil {
					return nil, err
				}
				results = append(results, result2.Result)
			}

			// Common activity after branch
			result4, err := flows.ExecuteActivity(ctx, SequenceActivity, &SimpleActivityInput{
				Operation: "after_branch",
				Index:     1,
			})
			if err != nil {
				return nil, err
			}
			results = append(results, result4.Result)

			return &BranchOutput{
				BranchTaken:     branchTaken,
				ActivityResults: results,
			}, nil
		},
	)

	// Test both branches
	for _, takeBranchA := range []bool{true, false} {
		t.Run(fmt.Sprintf("Branch_%v", takeBranchA), func(t *testing.T) {
			tenantID := uuid.New()
			ctx := flows.WithTenantID(context.Background(), tenantID)

			exec, err := flows.Start(ctx, branchWorkflow, &BranchInput{
				TakeBranchA: takeBranchA,
			})
			require.NoError(t, err)

			// Start worker
			worker := flows.NewWorker(pool, flows.WorkerConfig{
				Concurrency:   2,
				WorkflowNames: []string{"conditional-branch-workflow"},
				PollInterval:  100 * time.Millisecond,
				TenantID:      tenantID,
			})
			defer worker.Stop()

			workerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			go func() {
				_ = worker.Run(workerCtx)
			}()

			// Wait for completion
			result, err := exec.Get(ctx)
			require.NoError(t, err)
			require.NotNil(t, result)

			if takeBranchA {
				assert.Equal(t, "branch_a", result.BranchTaken)
				assert.Len(t, result.ActivityResults, 4)
				assert.Equal(t, "op_before_branch_1", result.ActivityResults[0])
				assert.Equal(t, "op_branch_a_1", result.ActivityResults[1])
				assert.Equal(t, "op_branch_a_2", result.ActivityResults[2])
				assert.Equal(t, "op_after_branch_1", result.ActivityResults[3])
			} else {
				assert.Equal(t, "branch_b", result.BranchTaken)
				assert.Len(t, result.ActivityResults, 3)
				assert.Equal(t, "op_before_branch_1", result.ActivityResults[0])
				assert.Equal(t, "op_branch_b_1", result.ActivityResults[1])
				assert.Equal(t, "op_after_branch_1", result.ActivityResults[2])
			}
		})
	}
}
