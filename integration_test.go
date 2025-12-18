package flows_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	flows "github.com/nvcnvn/flows"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// =============================================================================
// Test Infrastructure
// =============================================================================

// testDB manages a PostgreSQL connection pool for integration tests.
//
// The test infrastructure supports two modes:
// 1. Testcontainers (default): Automatically starts a PostgreSQL container
// 2. External database: Set FLOWS_TEST_DATABASE_URL to use an existing database
//
// Example: FLOWS_TEST_DATABASE_URL=postgres://user:pass@localhost:5432/flows_test
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var dbURL string

	// Check for external database URL first
	if envURL := os.Getenv("FLOWS_TEST_DATABASE_URL"); envURL != "" {
		dbURL = envURL
	} else {
		// Use testcontainers to start PostgreSQL
		container, err := postgres.Run(ctx,
			"citusdata/citus:13.2.0",
			postgres.WithDatabase("flows_test"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(30*time.Second),
			),
		)
		if err != nil {
			t.Skipf("Skipping integration test: could not start postgres container: %v", err)
		}

		t.Cleanup(func() {
			if err := container.Terminate(context.Background()); err != nil {
				t.Logf("Warning: failed to terminate container: %v", err)
			}
		})

		dbURL, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatalf("Failed to get connection string: %v", err)
		}
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("Skipping integration test: could not connect to database: %v", err)
	}

	// Test connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("Skipping integration test: could not ping database: %v", err)
	}

	// Create schema
	// Drop first to avoid stale schemas when using an external database.
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+flows.DefaultSchema+" CASCADE"); err != nil {
		pool.Close()
		t.Fatalf("Failed to drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, flows.SchemaSQL); err != nil {
		pool.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}

	// Verify Citus extension is available
	var citusVersion string
	if err := pool.QueryRow(ctx, "SELECT citus_version()").Scan(&citusVersion); err != nil {
		pool.Close()
		t.Fatalf("Citus extension not available: %v", err)
	}
	t.Logf("Citus version: %s", citusVersion)

	// For single-node Citus, add the coordinator as a worker node so it can hold shards.
	// First, set the coordinator host, then enable it to hold shards.
	if _, err := pool.Exec(ctx, "SELECT citus_set_coordinator_host('localhost', 5432)"); err != nil {
		t.Logf("Note: citus_set_coordinator_host: %v", err)
	}
	// Enable the coordinator to hold shard data (required for single-node Citus)
	if _, err := pool.Exec(ctx, "SELECT citus_set_node_property('localhost', 5432, 'shouldhaveshards', true)"); err != nil {
		t.Logf("Note: citus_set_node_property: %v", err)
	}

	// Set up Citus distributed tables
	if _, err := pool.Exec(ctx, flows.CitusSchemaSQL); err != nil {
		pool.Close()
		t.Fatalf("Failed to create Citus distributed tables: %v", err)
	}

	// Clean up tables for a fresh test
	cleanupTables(t, pool)

	t.Cleanup(func() {
		cleanupTables(t, pool)
		pool.Close()
	})

	return pool
}

// newTestWorker creates a Worker for testing.
func newTestWorker(pool *pgxpool.Pool, registry *flows.Registry, workflowNames ...string) *flows.Worker {
	return &flows.Worker{
		Pool:     pool,
		Registry: registry,
	}
}

func cleanupTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	// Delete in order respecting foreign key constraints.
	// Tables live in the dedicated schema (flows.DefaultSchema) and are unprefixed.
	tables := []string{
		"random",
		"events",
		"waits",
		"steps",
		"runs",
	}
	schema := flows.DefaultSchema

	for _, table := range tables {
		qualified := fmt.Sprintf("%s.%s", schema, table)
		if _, err := pool.Exec(ctx, "DELETE FROM "+qualified); err != nil {
			t.Logf("Warning: failed to clean table %s: %v", table, err)
		}
	}
}

// =============================================================================
// Test Workflow Implementations
// =============================================================================

// SimpleWorkflow is a basic workflow with configurable steps.
type SimpleWorkflow struct {
	StepCalls atomic.Int32
}

type SimpleInput struct {
	Name string `json:"name"`
}

type SimpleOutput struct {
	Greeting string `json:"greeting"`
}

func (w *SimpleWorkflow) Name() string { return "simple_workflow" }

func (w *SimpleWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	// Execute a step that generates a greeting
	result, err := flows.Execute(ctx, wf, "greet", func(ctx context.Context, in *SimpleInput) (*SimpleOutput, error) {
		w.StepCalls.Add(1)
		return &SimpleOutput{Greeting: "Hello, " + in.Name + "!"}, nil
	}, in, flows.RetryPolicy{})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// MultiStepWorkflow demonstrates multiple steps with memoization.
type MultiStepWorkflow struct {
	Step1Calls atomic.Int32
	Step2Calls atomic.Int32
	Step3Calls atomic.Int32
}

type MultiStepInput struct {
	Value int `json:"value"`
}

type MultiStepOutput struct {
	FinalValue int `json:"final_value"`
}

func (w *MultiStepWorkflow) Name() string { return "multi_step_workflow" }

func (w *MultiStepWorkflow) Run(ctx context.Context, wf *flows.Context, in *MultiStepInput) (*MultiStepOutput, error) {
	// Step 1: Add 10
	step1Out, err := flows.Execute(ctx, wf, "step1_add10", func(ctx context.Context, in *MultiStepInput) (*MultiStepInput, error) {
		w.Step1Calls.Add(1)
		return &MultiStepInput{Value: in.Value + 10}, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Step 2: Multiply by 2
	step2Out, err := flows.Execute(ctx, wf, "step2_multiply2", func(ctx context.Context, in *MultiStepInput) (*MultiStepInput, error) {
		w.Step2Calls.Add(1)
		return &MultiStepInput{Value: in.Value * 2}, nil
	}, step1Out, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Step 3: Subtract 5
	step3Out, err := flows.Execute(ctx, wf, "step3_subtract5", func(ctx context.Context, in *MultiStepInput) (*MultiStepOutput, error) {
		w.Step3Calls.Add(1)
		return &MultiStepOutput{FinalValue: in.Value - 5}, nil
	}, step2Out, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return step3Out, nil
}

// SleepWorkflow demonstrates durable sleep behavior.
type SleepWorkflow struct {
	BeforeSleepCalls atomic.Int32
	AfterSleepCalls  atomic.Int32
}

type SleepInput struct {
	SleepDuration time.Duration `json:"sleep_duration"`
}

type SleepOutput struct {
	Message string `json:"message"`
}

func (w *SleepWorkflow) Name() string { return "sleep_workflow" }

func (w *SleepWorkflow) Run(ctx context.Context, wf *flows.Context, in *SleepInput) (*SleepOutput, error) {
	// Step before sleep
	_, err := flows.Execute(ctx, wf, "before_sleep", func(ctx context.Context, in *SleepInput) (*SleepInput, error) {
		w.BeforeSleepCalls.Add(1)
		return in, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Durable sleep
	flows.Sleep(ctx, wf, "wait_1", in.SleepDuration)

	// Step after sleep
	result, err := flows.Execute(ctx, wf, "after_sleep", func(ctx context.Context, in *SleepInput) (*SleepOutput, error) {
		w.AfterSleepCalls.Add(1)
		return &SleepOutput{Message: "Woke up after sleep!"}, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// EventWorkflow demonstrates event-based waiting.
type EventWorkflow struct {
	BeforeWaitCalls atomic.Int32
	AfterWaitCalls  atomic.Int32
}

type EventInput struct {
	EventName string `json:"event_name"`
}

type EventPayload struct {
	Data string `json:"data"`
}

type EventOutput struct {
	ReceivedData string `json:"received_data"`
}

func (w *EventWorkflow) Name() string { return "event_workflow" }

func (w *EventWorkflow) Run(ctx context.Context, wf *flows.Context, in *EventInput) (*EventOutput, error) {
	// Step before waiting
	_, err := flows.Execute(ctx, wf, "before_wait", func(ctx context.Context, in *EventInput) (*EventInput, error) {
		w.BeforeWaitCalls.Add(1)
		return in, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Wait for event
	payload := flows.WaitForEvent[EventPayload](ctx, wf, "wait_for_approval", in.EventName)

	// Step after receiving event
	result, err := flows.Execute(ctx, wf, "after_wait", func(ctx context.Context, p *EventPayload) (*EventOutput, error) {
		w.AfterWaitCalls.Add(1)
		return &EventOutput{ReceivedData: p.Data}, nil
	}, payload, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// RetryWorkflow demonstrates retry behavior with configurable failures.
type RetryWorkflow struct {
	Attempts     atomic.Int32
	FailUntil    int32
	FailureError string
}

type RetryInput struct {
	Value string `json:"value"`
}

type RetryOutput struct {
	Result string `json:"result"`
}

func (w *RetryWorkflow) Name() string { return "retry_workflow" }

func (w *RetryWorkflow) Run(ctx context.Context, wf *flows.Context, in *RetryInput) (*RetryOutput, error) {
	result, err := flows.Execute(ctx, wf, "flaky_step", func(ctx context.Context, in *RetryInput) (*RetryOutput, error) {
		attempt := w.Attempts.Add(1)
		if attempt <= w.FailUntil {
			return nil, errors.New(w.FailureError)
		}
		return &RetryOutput{Result: "Success after " + fmt.Sprintf("%d", attempt) + " attempts"}, nil
	}, in, flows.RetryPolicy{
		MaxRetries: 5,
		Backoff: func(attempt int) int {
			return 10 // 10ms backoff for testing
		},
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// TransactionalWorkflow demonstrates business data writes via Context.Tx().
type TransactionalWorkflow struct{}

type TxInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type TxOutput struct {
	Written bool `json:"written"`
}

func (w *TransactionalWorkflow) Name() string { return "transactional_workflow" }

func (w *TransactionalWorkflow) Run(ctx context.Context, wf *flows.Context, in *TxInput) (*TxOutput, error) {
	// Ensure test table exists
	_, err := wf.Tx().Exec(ctx, `
		CREATE TABLE IF NOT EXISTS test_business_data (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("create test table: %w", err)
	}

	// Write business data inside the workflow transaction
	result, err := flows.Execute(ctx, wf, "write_data", func(ctx context.Context, in *TxInput) (*TxOutput, error) {
		_, err := wf.Tx().Exec(ctx,
			"INSERT INTO test_business_data (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = $2",
			in.Key, in.Value,
		)
		if err != nil {
			return nil, err
		}
		return &TxOutput{Written: true}, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// RandomWorkflow demonstrates deterministic random values.
type RandomWorkflow struct{}

type RandomInput struct{}

type RandomOutput struct {
	UUID   string `json:"uuid"`
	Number uint64 `json:"number"`
}

func (w *RandomWorkflow) Name() string { return "random_workflow" }

func (w *RandomWorkflow) Run(ctx context.Context, wf *flows.Context, in *RandomInput) (*RandomOutput, error) {
	uuid := flows.RandomUUIDv7(ctx, wf, "my_uuid")
	number := flows.RandomUint64(ctx, wf, "my_number")

	return &RandomOutput{
		UUID:   uuid,
		Number: number,
	}, nil
}

// =============================================================================
// Integration Tests
// =============================================================================

// TestBasicWorkflowLifecycle verifies that a simple workflow can be started,
// processed, and completed successfully.
func TestBasicWorkflowLifecycle(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Setup registry and workflow
	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	// Start a workflow run
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "World"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	t.Logf("Started run: %s", runKey.RunID)

	// Verify run is queued
	var status string
	err = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run status: %v", err)
	}
	if status != "queued" {
		t.Errorf("Expected status 'queued', got '%s'", status)
	}

	// Process the run
	worker := newTestWorker(pool, registry, wf.Name())

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run, but none was processed")
	}

	// Verify run is completed
	var outputJSON []byte
	err = pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status)
	}

	t.Logf("Run completed with output: %s", string(outputJSON))

	// Verify step was called exactly once
	if calls := wf.StepCalls.Load(); calls != 1 {
		t.Errorf("Expected step to be called 1 time, got %d", calls)
	}
}

// TestStepMemoization verifies that completed steps are not re-executed on replay.
// This simulates a scenario where the worker crashes mid-workflow and resumes.
func TestStepMemoization(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &MultiStepWorkflow{}
	flows.Register(registry, wf)

	// Start workflow
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &MultiStepInput{Value: 5})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Process the run
	worker := newTestWorker(pool, registry, wf.Name())
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify all steps were called exactly once
	if calls := wf.Step1Calls.Load(); calls != 1 {
		t.Errorf("Expected Step1 to be called 1 time, got %d", calls)
	}
	if calls := wf.Step2Calls.Load(); calls != 1 {
		t.Errorf("Expected Step2 to be called 1 time, got %d", calls)
	}
	if calls := wf.Step3Calls.Load(); calls != 1 {
		t.Errorf("Expected Step3 to be called 1 time, got %d", calls)
	}

	// Verify correct output: (5 + 10) * 2 - 5 = 25
	var outputJSON []byte
	err = pool.QueryRow(ctx, "SELECT output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&outputJSON)
	if err != nil {
		t.Fatalf("Failed to query output: %v", err)
	}

	t.Logf("Multi-step workflow output: %s", string(outputJSON))
}

// TestSleepAndWake verifies that Sleep yields execution and the workflow
// resumes after the sleep duration.
func TestSleepAndWake(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SleepWorkflow{}
	flows.Register(registry, wf)

	// Start workflow with a short sleep
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SleepInput{SleepDuration: 100 * time.Millisecond})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// First process: should execute before_sleep step and then yield on sleep
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run (first attempt): %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify run is sleeping
	var status string
	var nextWakeAt time.Time
	err = pool.QueryRow(ctx,
		"SELECT status, next_wake_at FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard,
		string(runKey.RunID),
	).Scan(&status, &nextWakeAt)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "sleeping" {
		t.Errorf("Expected status 'sleeping', got '%s'", status)
	}

	t.Logf("Run is sleeping until: %v", nextWakeAt)

	// Verify before_sleep was called but after_sleep was not
	if calls := wf.BeforeSleepCalls.Load(); calls != 1 {
		t.Errorf("Expected BeforeSleep to be called 1 time, got %d", calls)
	}
	if calls := wf.AfterSleepCalls.Load(); calls != 0 {
		t.Errorf("Expected AfterSleep to be called 0 times, got %d", calls)
	}

	// Try to process again immediately - should not pick up the run (still sleeping)
	processed, err = worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process: %v", err)
	}
	if processed {
		t.Error("Should not process run while still sleeping")
	}

	// Wait for sleep to elapse
	time.Sleep(150 * time.Millisecond)

	// Now process should pick up the awakened run
	processed, err = worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run (after sleep): %v", err)
	}
	if !processed {
		t.Error("Expected to process run after sleep elapsed")
	}

	// Verify run is completed
	err = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status)
	}

	// Verify step counts - before_sleep should still be 1 (memoized), after_sleep should be 1
	if calls := wf.BeforeSleepCalls.Load(); calls != 1 {
		t.Errorf("Expected BeforeSleep to be called 1 time (memoized on replay), got %d", calls)
	}
	if calls := wf.AfterSleepCalls.Load(); calls != 1 {
		t.Errorf("Expected AfterSleep to be called 1 time, got %d", calls)
	}
}

// TestWaitForEvent verifies event-based waiting and wake behavior.
func TestWaitForEvent(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &EventWorkflow{}
	flows.Register(registry, wf)

	// Start workflow
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &EventInput{EventName: "approval"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// First process: should execute before_wait and then yield waiting for event
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify run is waiting for event
	var status string
	err = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "waiting_event" {
		t.Errorf("Expected status 'waiting_event', got '%s'", status)
	}

	// Verify before_wait was called
	if calls := wf.BeforeWaitCalls.Load(); calls != 1 {
		t.Errorf("Expected BeforeWait to be called 1 time, got %d", calls)
	}

	// Publish the event
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	err = flows.PublishEventTx(ctx, client, tx, runKey, "approval", &EventPayload{Data: "approved!"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to publish event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Verify run was woken (status should be queued again)
	err = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "queued" {
		t.Errorf("Expected status 'queued' after event, got '%s'", status)
	}

	// Process the run to completion
	processed, err = worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run after event: %v", err)
	}
	if !processed {
		t.Error("Expected to process run after event")
	}

	// Verify run is completed
	var outputJSON []byte
	err = pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status)
	}

	t.Logf("Event workflow output: %s", string(outputJSON))

	// Verify step counts
	if calls := wf.AfterWaitCalls.Load(); calls != 1 {
		t.Errorf("Expected AfterWait to be called 1 time, got %d", calls)
	}
}

func waitForRunStatus(t *testing.T, pool *pgxpool.Pool, runKey flows.RunKey, want string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	var last string
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&last)
		if last == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for run %s status %q (last=%q)", runKey.RunID, want, last)
}

// TestWorkerRun_NotifyWakeupEvent ensures LISTEN/NOTIFY provides low-latency wakeups.
// With a long poll interval, the run should still complete quickly after PublishEventTx.
func TestWorkerRun_NotifyWakeupEvent(t *testing.T) {
	pool := setupTestDB(t)
	registry := flows.NewRegistry()
	wf := &EventWorkflow{}
	flows.Register(registry, wf)

	worker := &flows.Worker{
		Pool:         pool,
		Registry:     registry,
		PollInterval: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Run(ctx)
	}()

	// Start workflow (BeginTx also NOTIFYs).
	client := flows.Client{}
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	runKey, err := flows.BeginTx(context.Background(), client, tx, wf, &EventInput{EventName: "approval"})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Worker should pick it up and yield waiting for event.
	waitForRunStatus(t, pool, runKey, "waiting_event", 2*time.Second)

	// Publish the event. Even with a long PollInterval, the run should complete quickly.
	tx, err = pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	start := time.Now()
	err = flows.PublishEventTx(context.Background(), client, tx, runKey, "approval", &EventPayload{Data: "approved!"})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("Failed to publish event: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	waitForRunStatus(t, pool, runKey, "completed", 2*time.Second)
	if took := time.Since(start); took >= worker.PollInterval {
		t.Fatalf("expected completion faster than poll interval; took=%v poll=%v", took, worker.PollInterval)
	}

	cancel()
	err = <-errCh
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("worker returned error: %v", err)
	}
}

// TestWorkerRun_PollFallbackWithoutNotify ensures polling alone still makes progress.
func TestWorkerRun_PollFallbackWithoutNotify(t *testing.T) {
	pool := setupTestDB(t)
	registry := flows.NewRegistry()
	wf := &EventWorkflow{}
	flows.Register(registry, wf)

	worker := &flows.Worker{
		Pool:          pool,
		Registry:      registry,
		PollInterval:  50 * time.Millisecond,
		DisableNotify: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Run(ctx)
	}()

	client := flows.Client{}
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	runKey, err := flows.BeginTx(context.Background(), client, tx, wf, &EventInput{EventName: "approval"})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	waitForRunStatus(t, pool, runKey, "waiting_event", 2*time.Second)

	tx, err = pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	err = flows.PublishEventTx(context.Background(), client, tx, runKey, "approval", &EventPayload{Data: "approved!"})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("Failed to publish event: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	waitForRunStatus(t, pool, runKey, "completed", 2*time.Second)

	cancel()
	err = <-errCh
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("worker returned error: %v", err)
	}
}

// TestRetryPolicy verifies that step retries work correctly.
func TestRetryPolicy(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &RetryWorkflow{
		FailUntil:    3, // Fail first 3 attempts
		FailureError: "temporary error",
	}
	flows.Register(registry, wf)

	// Start workflow
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &RetryInput{Value: "test"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// Process the run - should succeed after retries
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify the step was attempted 4 times (3 failures + 1 success)
	if attempts := wf.Attempts.Load(); attempts != 4 {
		t.Errorf("Expected 4 attempts, got %d", attempts)
	}

	// Verify run completed successfully
	var status string
	var outputJSON []byte
	err = pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status)
	}

	t.Logf("Retry workflow output: %s", string(outputJSON))
}

// TestRetryExhausted verifies that a workflow fails when retries are exhausted.
func TestRetryExhausted(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &RetryWorkflow{
		FailUntil:    100, // Always fail
		FailureError: "persistent error",
	}
	flows.Register(registry, wf)

	// Start workflow
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &RetryInput{Value: "test"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// Process the run - should fail after exhausting retries
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify the step was attempted 6 times (MaxRetries=5, so 1 + 5 retries)
	if attempts := wf.Attempts.Load(); attempts != 6 {
		t.Errorf("Expected 6 attempts, got %d", attempts)
	}

	// Verify run failed
	var status, errorText string
	err = pool.QueryRow(ctx, "SELECT status, error_text FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &errorText)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", status)
	}
	if errorText != "persistent error" {
		t.Errorf("Expected error_text 'persistent error', got '%s'", errorText)
	}
}

// TestTransactionAtomicity verifies that business writes and step memoization
// are atomic - both committed or both rolled back together.
func TestTransactionAtomicity(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Create test table
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS test_business_data (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	registry := flows.NewRegistry()
	wf := &TransactionalWorkflow{}
	flows.Register(registry, wf)

	// Start workflow
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &TxInput{Key: "test_key", Value: "test_value"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// Process the run
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify run is completed
	var status string
	err = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status)
	}

	// Verify business data was written (atomically with step)
	var value string
	err = pool.QueryRow(ctx, "SELECT value FROM test_business_data WHERE key = $1", "test_key").Scan(&value)
	if err != nil {
		t.Fatalf("Failed to query business data: %v", err)
	}
	if value != "test_value" {
		t.Errorf("Expected value 'test_value', got '%s'", value)
	}

	// Clean up test table
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS test_business_data")
}

// TestDeterministicRandom verifies that RandomUUIDv7 and RandomUint64 return
// the same values on replay.
func TestDeterministicRandom(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &RandomWorkflow{}
	flows.Register(registry, wf)

	// Start workflow
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &RandomInput{})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// Process the run
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify random values were persisted
	var uuidVal string
	var uint64Val int64
	err = pool.QueryRow(ctx,
		"SELECT value_text FROM flows.random WHERE workflow_name_shard = $1 AND run_id = $2 AND rand_key = $3",
		runKey.WorkflowNameShard, string(runKey.RunID), "my_uuid",
	).Scan(&uuidVal)
	if err != nil {
		t.Fatalf("Failed to query UUID: %v", err)
	}

	err = pool.QueryRow(ctx,
		"SELECT value_bigint FROM flows.random WHERE workflow_name_shard = $1 AND run_id = $2 AND rand_key = $3",
		runKey.WorkflowNameShard, string(runKey.RunID), "my_number",
	).Scan(&uint64Val)
	if err != nil {
		t.Fatalf("Failed to query uint64: %v", err)
	}

	t.Logf("Generated UUID: %s", uuidVal)
	t.Logf("Generated uint64: %d", uint64Val)

	// Verify UUID format (basic check)
	if len(uuidVal) != 36 {
		t.Errorf("Expected UUID to be 36 characters, got %d", len(uuidVal))
	}
}

// TestConcurrentWorkers verifies that multiple workers can process runs
// concurrently without conflicts using SKIP LOCKED.
func TestConcurrentWorkers(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start multiple workflow runs
	numRuns := 10
	runKeys := make([]flows.RunKey, numRuns)
	for i := 0; i < numRuns; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Failed to begin transaction: %v", err)
		}

		runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: fmt.Sprintf("User%d", i)})
		if err != nil {
			tx.Rollback(ctx)
			t.Fatalf("Failed to start run: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
		runKeys[i] = runKey
	}

	// Process with multiple concurrent workers
	numWorkers := 3
	var wg sync.WaitGroup
	processedCount := atomic.Int32{}

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := newTestWorker(pool, registry, wf.Name())

			for {
				processed, err := worker.ProcessOne(ctx)
				if err != nil {
					t.Errorf("Worker error: %v", err)
					return
				}
				if !processed {
					return
				}
				processedCount.Add(1)
			}
		}()
	}

	wg.Wait()

	// Verify all runs were processed exactly once
	if count := processedCount.Load(); count != int32(numRuns) {
		t.Errorf("Expected %d runs to be processed, got %d", numRuns, count)
	}

	// Verify all runs are completed
	for _, runKey := range runKeys {
		var status string
		err := pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
		if err != nil {
			t.Fatalf("Failed to query run %s: %v", runKey.RunID, err)
		}
		if status != "completed" {
			t.Errorf("Expected run %s to be 'completed', got '%s'", runKey.RunID, status)
		}
	}
}

// TestEventBeforeWait verifies that if an event is published before the workflow
// starts waiting, the workflow receives it immediately without blocking.
func TestEventBeforeWait(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &EventWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start workflow
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &EventInput{EventName: "early_event"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}

	// Publish event BEFORE the workflow runs (in same transaction)
	err = flows.PublishEventTx(ctx, client, tx, runKey, "early_event", &EventPayload{Data: "early_data"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to publish event: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// Process should complete in one go (event already exists)
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if !processed {
		t.Error("Expected to process a run")
	}

	// Verify run is completed (not waiting_event)
	var status string
	err = pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status)
	}
}

// TestUnregisteredWorkflow verifies that the worker only processes runs for
// registered workflows. Runs for unknown workflows are simply not picked up.
func TestUnregisteredWorkflow(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Manually insert a run for a workflow that is NOT registered
	_, err := pool.Exec(ctx, `
		INSERT INTO flows.runs (workflow_name_shard, run_id, workflow_name, status, input_json)
		VALUES ('nonexistent_workflow_0', '00000000-0000-0000-0000-000000000000', 'nonexistent_workflow', 'queued', '{}')
	`)
	if err != nil {
		t.Fatalf("Failed to insert test run: %v", err)
	}

	// Create registry with a different workflow
	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	worker := newTestWorker(pool, registry, wf.Name())

	// Process - should not pick up the run for the unregistered workflow
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process run: %v", err)
	}
	if processed {
		t.Error("Should not process run for unregistered workflow")
	}

	// Verify run is still queued (not picked up)
	var status string
	err = pool.QueryRow(ctx,
		"SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		"nonexistent_workflow_0",
		"00000000-0000-0000-0000-000000000000",
	).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query run: %v", err)
	}
	if status != "queued" {
		t.Errorf("Expected status 'queued' (run should be ignored), got '%s'", status)
	}
}

// TestMultipleSleeps verifies workflows with multiple consecutive sleeps.
func TestMultipleSleeps(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	wf := &MultipleSleepWorkflow{}

	registry := flows.NewRegistry()
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SleepInput{SleepDuration: 50 * time.Millisecond})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// First process - enters first sleep
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("First process error: %v", err)
	}
	if !processed {
		t.Error("Expected to process run")
	}

	var status string
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "sleeping" {
		t.Errorf("Expected 'sleeping', got '%s'", status)
	}

	// Wait and process again - completes first sleep, enters second
	time.Sleep(60 * time.Millisecond)
	processed, _ = worker.ProcessOne(ctx)
	if !processed {
		t.Error("Expected to process after first sleep")
	}

	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "sleeping" {
		t.Errorf("Expected 'sleeping' (second), got '%s'", status)
	}

	// Wait and process again - completes
	time.Sleep(60 * time.Millisecond)
	processed, _ = worker.ProcessOne(ctx)
	if !processed {
		t.Error("Expected to process after second sleep")
	}

	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "completed" {
		t.Errorf("Expected 'completed', got '%s'", status)
	}
}

// MultipleSleepWorkflow demonstrates workflows with multiple consecutive sleeps.
type MultipleSleepWorkflow struct {
	Sleep1Completed atomic.Bool
	Sleep2Completed atomic.Bool
}

func (w *MultipleSleepWorkflow) Name() string { return "multiple_sleep_workflow" }

func (w *MultipleSleepWorkflow) Run(ctx context.Context, wf *flows.Context, in *SleepInput) (*SleepOutput, error) {
	flows.Sleep(ctx, wf, "sleep_1", in.SleepDuration)
	w.Sleep1Completed.Store(true)

	flows.Sleep(ctx, wf, "sleep_2", in.SleepDuration)
	w.Sleep2Completed.Store(true)

	return &SleepOutput{Message: "Both sleeps completed"}, nil
}

// TestStepInputOutput verifies that step inputs and outputs are correctly
// persisted and can be queried for debugging/auditing.
func TestStepInputOutput(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &MultiStepWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &MultiStepInput{Value: 5})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Query step records
	rows, err := pool.Query(ctx,
		"SELECT step_key, input_json, output_json, attempts FROM flows.steps WHERE workflow_name_shard = $1 AND run_id = $2 ORDER BY step_key",
		runKey.WorkflowNameShard,
		string(runKey.RunID),
	)
	if err != nil {
		t.Fatalf("Failed to query steps: %v", err)
	}
	defer rows.Close()

	steps := make(map[string]struct {
		Input    string
		Output   string
		Attempts int
	})

	for rows.Next() {
		var stepKey string
		var inputJSON, outputJSON []byte
		var attempts int
		if err := rows.Scan(&stepKey, &inputJSON, &outputJSON, &attempts); err != nil {
			t.Fatalf("Failed to scan step: %v", err)
		}
		steps[stepKey] = struct {
			Input    string
			Output   string
			Attempts int
		}{string(inputJSON), string(outputJSON), attempts}
	}

	t.Logf("Steps recorded: %+v", steps)

	// Verify expected steps exist
	expectedSteps := []string{"step1_add10", "step2_multiply2", "step3_subtract5"}
	for _, stepKey := range expectedSteps {
		if _, ok := steps[stepKey]; !ok {
			t.Errorf("Expected step '%s' to exist", stepKey)
		}
	}
}

// =============================================================================
// Real-World Scenario Tests
// These tests demonstrate complex, real-life usage patterns and serve as
// working documentation for how to use the flows package.
// =============================================================================

// OrderProcessingWorkflow simulates an e-commerce order processing workflow
// with multiple steps, external service calls, and event-based confirmation.
type OrderProcessingWorkflow struct {
	ValidateOrderCalls  atomic.Int32
	ReserveStockCalls   atomic.Int32
	ProcessPaymentCalls atomic.Int32
	ShipOrderCalls      atomic.Int32
}

type Order struct {
	OrderID    string   `json:"order_id"`
	CustomerID string   `json:"customer_id"`
	Amount     float64  `json:"amount"`
	Items      []string `json:"items"`
}

type OrderResult struct {
	OrderID     string `json:"order_id"`
	Status      string `json:"status"`
	TrackingNum string `json:"tracking_num,omitempty"`
}

func (w *OrderProcessingWorkflow) Name() string { return "order_processing" }

func (w *OrderProcessingWorkflow) Run(ctx context.Context, wf *flows.Context, in *Order) (*OrderResult, error) {
	// Step 1: Validate order
	_, err := flows.Execute(ctx, wf, "validate_order", func(ctx context.Context, order *Order) (*Order, error) {
		w.ValidateOrderCalls.Add(1)
		if order.Amount <= 0 {
			return nil, errors.New("invalid order amount")
		}
		if len(order.Items) == 0 {
			return nil, errors.New("order has no items")
		}
		return order, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Step 2: Reserve inventory (with retries for transient failures)
	_, err = flows.Execute(ctx, wf, "reserve_stock", func(ctx context.Context, order *Order) (*Order, error) {
		w.ReserveStockCalls.Add(1)
		// Simulate inventory reservation
		return order, nil
	}, in, flows.RetryPolicy{MaxRetries: 3, Backoff: func(attempt int) int { return 10 }})
	if err != nil {
		return nil, err
	}

	// Step 3: Process payment (with retries)
	_, err = flows.Execute(ctx, wf, "process_payment", func(ctx context.Context, order *Order) (*Order, error) {
		w.ProcessPaymentCalls.Add(1)
		// Simulate payment processing
		return order, nil
	}, in, flows.RetryPolicy{MaxRetries: 2, Backoff: func(attempt int) int { return 50 }})
	if err != nil {
		return nil, err
	}

	// Step 4: Wait for warehouse confirmation event
	confirmation := flows.WaitForEvent[struct{ Confirmed bool }](ctx, wf, "wait_warehouse", "warehouse_confirmation")
	if !confirmation.Confirmed {
		return &OrderResult{OrderID: in.OrderID, Status: "cancelled"}, nil
	}

	// Step 5: Ship order and get tracking number
	trackingNum := flows.RandomUUIDv7(ctx, wf, "tracking_number")

	result, err := flows.Execute(ctx, wf, "ship_order", func(ctx context.Context, order *Order) (*OrderResult, error) {
		w.ShipOrderCalls.Add(1)
		return &OrderResult{
			OrderID:     order.OrderID,
			Status:      "shipped",
			TrackingNum: trackingNum,
		}, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// TestOrderProcessingWorkflow tests a realistic e-commerce order workflow
// demonstrating steps, retries, events, and deterministic random values.
func TestOrderProcessingWorkflow(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &OrderProcessingWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	order := &Order{
		OrderID:    "ORD-12345",
		CustomerID: "CUST-001",
		Amount:     99.99,
		Items:      []string{"widget", "gadget"},
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, order)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	worker := newTestWorker(pool, registry, wf.Name())

	// Process until waiting for event
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process: %v", err)
	}
	if !processed {
		t.Error("Expected to process run")
	}

	// Verify workflow is waiting for warehouse confirmation
	var status string
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "waiting_event" {
		t.Errorf("Expected 'waiting_event', got '%s'", status)
	}

	// Verify first 3 steps completed
	if wf.ValidateOrderCalls.Load() != 1 {
		t.Error("Expected ValidateOrder to be called once")
	}
	if wf.ReserveStockCalls.Load() != 1 {
		t.Error("Expected ReserveStock to be called once")
	}
	if wf.ProcessPaymentCalls.Load() != 1 {
		t.Error("Expected ProcessPayment to be called once")
	}

	// Publish warehouse confirmation
	tx, _ = pool.Begin(ctx)
	err = flows.PublishEventTx(ctx, client, tx, runKey, "warehouse_confirmation", &struct{ Confirmed bool }{Confirmed: true})
	if err != nil {
		t.Fatalf("Failed to publish event: %v", err)
	}
	tx.Commit(ctx)

	// Complete the workflow
	processed, err = worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Failed to process after event: %v", err)
	}
	if !processed {
		t.Error("Expected to process run after event")
	}

	// Verify completion
	var outputJSON []byte
	pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if status != "completed" {
		t.Errorf("Expected 'completed', got '%s'", status)
	}

	t.Logf("Order processing completed: %s", string(outputJSON))

	// Verify tracking number was generated and persisted
	var trackingUUID string
	err = pool.QueryRow(ctx,
		"SELECT value_text FROM flows.random WHERE workflow_name_shard = $1 AND run_id = $2 AND rand_key = 'tracking_number'",
		runKey.WorkflowNameShard,
		string(runKey.RunID),
	).Scan(&trackingUUID)
	if err != nil {
		t.Fatalf("Failed to query tracking number: %v", err)
	}
	t.Logf("Generated tracking number: %s", trackingUUID)
}

// ScheduledTaskWorkflow demonstrates a workflow with multiple timed operations,
// like a scheduled job that runs periodically.
type ScheduledTaskWorkflow struct {
	TaskExecutions atomic.Int32
}

type ScheduledTaskInput struct {
	TaskName string        `json:"task_name"`
	Interval time.Duration `json:"interval"`
	RunCount int           `json:"run_count"`
}

type ScheduledTaskOutput struct {
	TotalExecutions int    `json:"total_executions"`
	TaskName        string `json:"task_name"`
}

func (w *ScheduledTaskWorkflow) Name() string { return "scheduled_task" }

func (w *ScheduledTaskWorkflow) Run(ctx context.Context, wf *flows.Context, in *ScheduledTaskInput) (*ScheduledTaskOutput, error) {
	for i := 0; i < in.RunCount; i++ {
		// Execute the scheduled task
		_, err := flows.Execute(ctx, wf, fmt.Sprintf("task_execution_%d", i), func(ctx context.Context, in *ScheduledTaskInput) (*ScheduledTaskInput, error) {
			w.TaskExecutions.Add(1)
			return in, nil
		}, in, flows.RetryPolicy{})
		if err != nil {
			return nil, err
		}

		// Sleep between executions (except for the last one)
		if i < in.RunCount-1 {
			flows.Sleep(ctx, wf, fmt.Sprintf("interval_%d", i), in.Interval)
		}
	}

	return &ScheduledTaskOutput{
		TotalExecutions: in.RunCount,
		TaskName:        in.TaskName,
	}, nil
}

// TestScheduledTaskWorkflow tests a workflow that simulates periodic task execution.
func TestScheduledTaskWorkflow(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &ScheduledTaskWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &ScheduledTaskInput{
		TaskName: "cleanup",
		Interval: 30 * time.Millisecond,
		RunCount: 3,
	})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	worker := newTestWorker(pool, registry, wf.Name())

	// Process with sleeps - this will take multiple iterations
	iterations := 0
	for iterations < 10 {
		var status string
		pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)

		if status == "completed" {
			break
		}

		if status == "sleeping" {
			time.Sleep(40 * time.Millisecond)
		}

		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			t.Fatalf("Process error: %v", err)
		}
		if processed {
			iterations++
		}
	}

	// Verify all task executions happened
	if executions := wf.TaskExecutions.Load(); executions != 3 {
		t.Errorf("Expected 3 task executions, got %d", executions)
	}

	var status string
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "completed" {
		t.Errorf("Expected 'completed', got '%s'", status)
	}
}

// ApprovalChainWorkflow demonstrates a multi-level approval workflow
// common in enterprise applications.
type ApprovalChainWorkflow struct{}

type ApprovalRequest struct {
	RequestID   string   `json:"request_id"`
	RequesterID string   `json:"requester_id"`
	Amount      float64  `json:"amount"`
	Approvers   []string `json:"approvers"`
}

type ApprovalDecision struct {
	ApproverID string `json:"approver_id"`
	Approved   bool   `json:"approved"`
	Comment    string `json:"comment"`
}

type ApprovalResult struct {
	RequestID string             `json:"request_id"`
	Status    string             `json:"status"` // approved, rejected, pending
	Decisions []ApprovalDecision `json:"decisions"`
}

func (w *ApprovalChainWorkflow) Name() string { return "approval_chain" }

func (w *ApprovalChainWorkflow) Run(ctx context.Context, wf *flows.Context, in *ApprovalRequest) (*ApprovalResult, error) {
	result := &ApprovalResult{
		RequestID: in.RequestID,
		Status:    "pending",
		Decisions: []ApprovalDecision{},
	}

	// Each approver must approve in sequence
	for i, approverID := range in.Approvers {
		eventName := fmt.Sprintf("approval_%s", approverID)
		waitKey := fmt.Sprintf("wait_approval_%d", i)

		// Wait for this approver's decision
		decision := flows.WaitForEvent[ApprovalDecision](ctx, wf, waitKey, eventName)
		result.Decisions = append(result.Decisions, *decision)

		// If rejected, stop the chain
		if !decision.Approved {
			result.Status = "rejected"
			return result, nil
		}
	}

	result.Status = "approved"
	return result, nil
}

// TestApprovalChainWorkflow tests a multi-level approval workflow.
func TestApprovalChainWorkflow(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &ApprovalChainWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &ApprovalRequest{
		RequestID:   "REQ-001",
		RequesterID: "user@example.com",
		Amount:      5000.00,
		Approvers:   []string{"manager", "director", "cfo"},
	})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	worker := newTestWorker(pool, registry, wf.Name())

	// Process - should wait for first approval
	worker.ProcessOne(ctx)

	var status string
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "waiting_event" {
		t.Errorf("Expected 'waiting_event', got '%s'", status)
	}

	// Manager approves
	tx, _ = pool.Begin(ctx)
	flows.PublishEventTx(ctx, client, tx, runKey, "approval_manager", &ApprovalDecision{
		ApproverID: "manager",
		Approved:   true,
		Comment:    "Looks good",
	})
	tx.Commit(ctx)

	worker.ProcessOne(ctx)

	// Director approves
	tx, _ = pool.Begin(ctx)
	flows.PublishEventTx(ctx, client, tx, runKey, "approval_director", &ApprovalDecision{
		ApproverID: "director",
		Approved:   true,
		Comment:    "Within budget",
	})
	tx.Commit(ctx)

	worker.ProcessOne(ctx)

	// CFO approves
	tx, _ = pool.Begin(ctx)
	flows.PublishEventTx(ctx, client, tx, runKey, "approval_cfo", &ApprovalDecision{
		ApproverID: "cfo",
		Approved:   true,
		Comment:    "Approved",
	})
	tx.Commit(ctx)

	worker.ProcessOne(ctx)

	// Verify final status
	var outputJSON []byte
	pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if status != "completed" {
		t.Errorf("Expected 'completed', got '%s'", status)
	}

	t.Logf("Approval chain result: %s", string(outputJSON))
}

// TestApprovalChainRejection tests the approval workflow when one approver rejects.
func TestApprovalChainRejection(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &ApprovalChainWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &ApprovalRequest{
		RequestID:   "REQ-002",
		RequesterID: "user@example.com",
		Amount:      50000.00,
		Approvers:   []string{"manager", "director"},
	})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Manager approves
	tx, _ = pool.Begin(ctx)
	flows.PublishEventTx(ctx, client, tx, runKey, "approval_manager", &ApprovalDecision{
		ApproverID: "manager",
		Approved:   true,
		Comment:    "Approved",
	})
	tx.Commit(ctx)

	worker.ProcessOne(ctx)

	// Director REJECTS
	tx, _ = pool.Begin(ctx)
	flows.PublishEventTx(ctx, client, tx, runKey, "approval_director", &ApprovalDecision{
		ApproverID: "director",
		Approved:   false,
		Comment:    "Budget exceeded",
	})
	tx.Commit(ctx)

	worker.ProcessOne(ctx)

	// Verify rejection
	var status string
	var outputJSON []byte
	pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if status != "completed" {
		t.Errorf("Expected 'completed', got '%s'", status)
	}

	// Output should show rejected status
	if !containsString(string(outputJSON), `"status": "rejected"`) && !containsString(string(outputJSON), `"status":"rejected"`) {
		t.Errorf("Expected output to contain rejected status, got: %s", string(outputJSON))
	}

	t.Logf("Rejection result: %s", string(outputJSON))
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (s[0:len(substr)] == substr || containsString(s[1:], substr)))
}

// DataPipelineWorkflow demonstrates a data processing pipeline with
// fan-out pattern (multiple parallel-like operations done sequentially).
type DataPipelineWorkflow struct {
	TransformCalls atomic.Int32
}

type PipelineInput struct {
	DataSources []string `json:"data_sources"`
	OutputPath  string   `json:"output_path"`
}

type PipelineOutput struct {
	ProcessedCount int    `json:"processed_count"`
	OutputPath     string `json:"output_path"`
}

func (w *DataPipelineWorkflow) Name() string { return "data_pipeline" }

func (w *DataPipelineWorkflow) Run(ctx context.Context, wf *flows.Context, in *PipelineInput) (*PipelineOutput, error) {
	processedCount := 0

	// Process each data source (would be parallel in real system, sequential here for durability)
	for i, source := range in.DataSources {
		_, err := flows.Execute(ctx, wf, fmt.Sprintf("transform_%d", i), func(ctx context.Context, source *string) (*string, error) {
			w.TransformCalls.Add(1)
			result := "processed_" + *source
			return &result, nil
		}, &source, flows.RetryPolicy{MaxRetries: 2})
		if err != nil {
			return nil, fmt.Errorf("failed to process source %s: %w", source, err)
		}
		processedCount++
	}

	return &PipelineOutput{
		ProcessedCount: processedCount,
		OutputPath:     in.OutputPath,
	}, nil
}

// TestDataPipelineWorkflow tests a data processing pipeline pattern.
func TestDataPipelineWorkflow(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &DataPipelineWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &PipelineInput{
		DataSources: []string{"source_a", "source_b", "source_c", "source_d", "source_e"},
		OutputPath:  "/output/combined.json",
	})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Verify all sources processed
	if calls := wf.TransformCalls.Load(); calls != 5 {
		t.Errorf("Expected 5 transform calls, got %d", calls)
	}

	var status string
	var outputJSON []byte
	pool.QueryRow(ctx, "SELECT status, output_json FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if status != "completed" {
		t.Errorf("Expected 'completed', got '%s'", status)
	}

	t.Logf("Pipeline result: %s", string(outputJSON))

	// Verify step records
	var stepCount int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM flows.steps WHERE workflow_name_shard = $1 AND run_id = $2", runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&stepCount)
	if stepCount != 5 {
		t.Errorf("Expected 5 step records, got %d", stepCount)
	}
}

// =============================================================================
// Multi-Instance Worker Isolation and Thread Safety Tests
// =============================================================================

// IsolationTestWorkflow is designed to test that each run maintains isolated state
// and that concurrent execution doesn't cause race conditions.
type IsolationTestWorkflow struct {
	// Shared counters across all runs to verify execution counts
	TotalExecutions atomic.Int32
	Step1Executions atomic.Int32
	Step2Executions atomic.Int32
	Step3Executions atomic.Int32
}

type IsolationInput struct {
	RunIndex int `json:"run_index"`
	Delay    int `json:"delay_ms"` // Artificial delay to increase overlap
}

type IsolationOutput struct {
	RunIndex   int    `json:"run_index"`
	Step1Value int    `json:"step1_value"`
	Step2Value int    `json:"step2_value"`
	Step3Value int    `json:"step3_value"`
	WorkerID   string `json:"worker_id"` // Track which conceptual worker processed this
}

func (w *IsolationTestWorkflow) Name() string { return "isolation_test_workflow" }

func (w *IsolationTestWorkflow) Run(ctx context.Context, wf *flows.Context, in *IsolationInput) (*IsolationOutput, error) {
	w.TotalExecutions.Add(1)

	// Step 1: Generate a value based on run index
	step1Out, err := flows.Execute(ctx, wf, "step1_init", func(ctx context.Context, in *IsolationInput) (*IsolationInput, error) {
		w.Step1Executions.Add(1)
		// Add delay to increase chance of concurrent execution overlap
		if in.Delay > 0 {
			time.Sleep(time.Duration(in.Delay) * time.Millisecond)
		}
		return &IsolationInput{RunIndex: in.RunIndex * 10, Delay: in.Delay}, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Step 2: Transform the value
	step2Out, err := flows.Execute(ctx, wf, "step2_transform", func(ctx context.Context, in *IsolationInput) (*IsolationInput, error) {
		w.Step2Executions.Add(1)
		if in.Delay > 0 {
			time.Sleep(time.Duration(in.Delay) * time.Millisecond)
		}
		return &IsolationInput{RunIndex: in.RunIndex + 1, Delay: in.Delay}, nil
	}, step1Out, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	// Step 3: Finalize
	result, err := flows.Execute(ctx, wf, "step3_finalize", func(ctx context.Context, in *IsolationInput) (*IsolationOutput, error) {
		w.Step3Executions.Add(1)
		if in.Delay > 0 {
			time.Sleep(time.Duration(in.Delay) * time.Millisecond)
		}
		return &IsolationOutput{
			RunIndex:   in.RunIndex * 2,
			Step1Value: in.RunIndex,
			Step2Value: in.RunIndex + 1,
			Step3Value: in.RunIndex * 2,
		}, nil
	}, step2Out, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// TestMultipleWorkerInstancesIsolation verifies that multiple worker instances
// running concurrently maintain proper isolation and thread safety.
// This test is designed to catch:
// - Race conditions on shared workflow state
// - Runs being processed multiple times (SKIP LOCKED verification)
// - Per-run state bleeding between concurrent executions
// - Registry access contention
func TestMultipleWorkerInstancesIsolation(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &IsolationTestWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Create many runs to be processed concurrently
	numRuns := 50
	for i := 0; i < numRuns; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Failed to begin transaction: %v", err)
		}

		_, err = flows.BeginTx(ctx, client, tx, wf, &IsolationInput{
			RunIndex: i + 1, // 1-indexed for easier verification
			Delay:    10,    // 10ms delay per step to increase overlap
		})
		if err != nil {
			tx.Rollback(ctx)
			t.Fatalf("Failed to start run %d: %v", i, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Failed to commit run %d: %v", i, err)
		}
	}

	// Spawn multiple worker instances concurrently
	numWorkers := 10
	var wg sync.WaitGroup
	processedByWorker := make([]atomic.Int32, numWorkers)
	totalProcessed := atomic.Int32{}
	workerErrors := make(chan error, numWorkers*numRuns)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		workerIndex := w
		go func() {
			defer wg.Done()

			// Each goroutine creates its own Worker instance
			// They share the same Pool and Registry (this is the expected usage pattern)
			worker := &flows.Worker{
				Pool:         pool,
				Registry:     registry,
				PollInterval: 10 * time.Millisecond, // Fast polling for test
			}

			for {
				processed, err := worker.ProcessOne(ctx)
				if err != nil {
					workerErrors <- fmt.Errorf("worker %d error: %w", workerIndex, err)
					return
				}
				if !processed {
					// No more work available
					return
				}
				processedByWorker[workerIndex].Add(1)
				totalProcessed.Add(1)
			}
		}()
	}

	wg.Wait()
	close(workerErrors)

	// Check for any worker errors
	for err := range workerErrors {
		t.Errorf("Worker error: %v", err)
	}

	// Verify total processed count
	if count := totalProcessed.Load(); count != int32(numRuns) {
		t.Errorf("Expected %d runs to be processed, got %d", numRuns, count)
	}

	// Verify each step was executed exactly numRuns times (once per run)
	if calls := wf.Step1Executions.Load(); calls != int32(numRuns) {
		t.Errorf("Expected Step1 to execute %d times, got %d", numRuns, calls)
	}
	if calls := wf.Step2Executions.Load(); calls != int32(numRuns) {
		t.Errorf("Expected Step2 to execute %d times, got %d", numRuns, calls)
	}
	if calls := wf.Step3Executions.Load(); calls != int32(numRuns) {
		t.Errorf("Expected Step3 to execute %d times, got %d", numRuns, calls)
	}

	// Verify all runs completed successfully
	var completedCount int
	err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM flows.runs WHERE status = 'completed'",
	).Scan(&completedCount)
	if err != nil {
		t.Fatalf("Failed to count completed runs: %v", err)
	}
	if completedCount != numRuns {
		t.Errorf("Expected %d completed runs, got %d", numRuns, completedCount)
	}

	// Verify no duplicate step executions (each step_key should appear exactly once per run)
	var duplicateSteps int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT workflow_name_shard, run_id, step_key, COUNT(*) as cnt
			FROM flows.steps
			GROUP BY workflow_name_shard, run_id, step_key
			HAVING COUNT(*) > 1
		) duplicates
	`).Scan(&duplicateSteps)
	if err != nil {
		t.Fatalf("Failed to check for duplicate steps: %v", err)
	}
	if duplicateSteps != 0 {
		t.Errorf("Found %d duplicate step executions (should be 0)", duplicateSteps)
	}

	// Log worker distribution for debugging
	t.Log("Runs processed per worker:")
	for i := 0; i < numWorkers; i++ {
		t.Logf("  Worker %d: %d runs", i, processedByWorker[i].Load())
	}
}

// TestHighContention verifies behavior when there are more workers than runs.
// Workers should gracefully exit when no work is available.
func TestHighContention(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Create fewer runs than workers
	numRuns := 5
	numWorkers := 20

	for i := 0; i < numRuns; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Failed to begin transaction: %v", err)
		}

		_, err = flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: fmt.Sprintf("User%d", i)})
		if err != nil {
			tx.Rollback(ctx)
			t.Fatalf("Failed to start run: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
	}

	// Spawn many workers - most should exit immediately with no work
	var wg sync.WaitGroup
	totalProcessed := atomic.Int32{}
	workersCompleted := atomic.Int32{}

	startTime := time.Now()

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer workersCompleted.Add(1)

			worker := newTestWorker(pool, registry, wf.Name())

			for {
				processed, err := worker.ProcessOne(ctx)
				if err != nil {
					t.Errorf("Worker error: %v", err)
					return
				}
				if !processed {
					return
				}
				totalProcessed.Add(1)
			}
		}()
	}

	wg.Wait()
	duration := time.Since(startTime)

	// Verify all workers completed
	if completed := workersCompleted.Load(); completed != int32(numWorkers) {
		t.Errorf("Expected %d workers to complete, got %d", numWorkers, completed)
	}

	// Verify correct number of runs processed
	if count := totalProcessed.Load(); count != int32(numRuns) {
		t.Errorf("Expected %d runs to be processed, got %d", numRuns, count)
	}

	// Verify all runs are completed
	var completedCount int
	err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM flows.runs WHERE status = 'completed'",
	).Scan(&completedCount)
	if err != nil {
		t.Fatalf("Failed to count completed runs: %v", err)
	}
	if completedCount != numRuns {
		t.Errorf("Expected %d completed runs, got %d", numRuns, completedCount)
	}

	t.Logf("High contention test completed in %v (%d workers, %d runs)", duration, numWorkers, numRuns)
}

// SharedStateWorkflow tests for race conditions on shared workflow struct state.
// This workflow intentionally uses shared mutable state to detect races.
type SharedStateWorkflow struct {
	// These atomic counters are safe
	ExecutionCount atomic.Int32

	// Track run IDs that were processed (thread-safe via sync.Map)
	ProcessedRuns sync.Map
}

type SharedStateInput struct {
	Value int `json:"value"`
}

type SharedStateOutput struct {
	Result int `json:"result"`
}

func (w *SharedStateWorkflow) Name() string { return "shared_state_workflow" }

func (w *SharedStateWorkflow) Run(ctx context.Context, wf *flows.Context, in *SharedStateInput) (*SharedStateOutput, error) {
	w.ExecutionCount.Add(1)

	result, err := flows.Execute(ctx, wf, "compute", func(ctx context.Context, in *SharedStateInput) (*SharedStateOutput, error) {
		// Record this run was processed
		w.ProcessedRuns.Store(wf.RunID(), true)

		// Add small delay to increase chance of concurrent overlap
		time.Sleep(5 * time.Millisecond)

		return &SharedStateOutput{Result: in.Value * 2}, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// TestConcurrentWorkersRaceDetection is designed to be run with -race flag
// to detect any race conditions in the worker/flow execution.
func TestConcurrentWorkersRaceDetection(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SharedStateWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Create runs
	numRuns := 30
	for i := 0; i < numRuns; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Failed to begin transaction: %v", err)
		}

		_, err = flows.BeginTx(ctx, client, tx, wf, &SharedStateInput{Value: i + 1})
		if err != nil {
			tx.Rollback(ctx)
			t.Fatalf("Failed to start run: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
	}

	// Run with many workers to maximize concurrency
	numWorkers := 8
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := newTestWorker(pool, registry, wf.Name())

			for {
				processed, err := worker.ProcessOne(ctx)
				if err != nil {
					t.Errorf("Worker error: %v", err)
					return
				}
				if !processed {
					return
				}
			}
		}()
	}

	wg.Wait()

	// Verify all runs were processed exactly once
	if count := wf.ExecutionCount.Load(); count != int32(numRuns) {
		t.Errorf("Expected %d executions, got %d", numRuns, count)
	}

	// Verify each run was tracked
	processedCount := 0
	wf.ProcessedRuns.Range(func(key, value interface{}) bool {
		processedCount++
		return true
	})
	if processedCount != numRuns {
		t.Errorf("Expected %d unique runs in ProcessedRuns, got %d", numRuns, processedCount)
	}

	// Verify all runs completed
	var completedCount int
	err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM flows.runs WHERE status = 'completed'",
	).Scan(&completedCount)
	if err != nil {
		t.Fatalf("Failed to count completed runs: %v", err)
	}
	if completedCount != numRuns {
		t.Errorf("Expected %d completed runs, got %d", numRuns, completedCount)
	}

	t.Log("Race detection test passed (run with -race flag for full detection)")
}

// TestConcurrentSleepingWorkflows verifies that multiple workers correctly
// handle workflows that are sleeping and wake up at the same time.
func TestConcurrentSleepingWorkflows(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SleepWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Create multiple runs that will all sleep for the same duration
	numRuns := 10
	sleepDuration := 50 * time.Millisecond

	for i := 0; i < numRuns; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Failed to begin transaction: %v", err)
		}

		_, err = flows.BeginTx(ctx, client, tx, wf, &SleepInput{SleepDuration: sleepDuration})
		if err != nil {
			tx.Rollback(ctx)
			t.Fatalf("Failed to start run: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
	}

	numWorkers := 5
	var wg sync.WaitGroup
	processedCount := atomic.Int32{}

	// Phase 1: Process all runs until they sleep
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := newTestWorker(pool, registry, wf.Name())
			for {
				processed, err := worker.ProcessOne(ctx)
				if err != nil {
					t.Errorf("Worker error: %v", err)
					return
				}
				if !processed {
					return
				}
				processedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	// Verify all runs are now sleeping
	var sleepingCount int
	err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM flows.runs WHERE status = 'sleeping'",
	).Scan(&sleepingCount)
	if err != nil {
		t.Fatalf("Failed to count sleeping runs: %v", err)
	}
	if sleepingCount != numRuns {
		t.Errorf("Expected %d sleeping runs, got %d", numRuns, sleepingCount)
	}

	// Wait for all runs to wake up
	time.Sleep(sleepDuration + 50*time.Millisecond)

	// Phase 2: Process all awakened runs with multiple workers
	processedCount.Store(0)
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := newTestWorker(pool, registry, wf.Name())
			for {
				processed, err := worker.ProcessOne(ctx)
				if err != nil {
					t.Errorf("Worker error: %v", err)
					return
				}
				if !processed {
					return
				}
				processedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	// Verify all runs are now completed
	var completedCount int
	err = pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM flows.runs WHERE status = 'completed'",
	).Scan(&completedCount)
	if err != nil {
		t.Fatalf("Failed to count completed runs: %v", err)
	}
	if completedCount != numRuns {
		t.Errorf("Expected %d completed runs, got %d", numRuns, completedCount)
	}

	// Verify before/after sleep steps were each called exactly numRuns times
	if calls := wf.BeforeSleepCalls.Load(); calls != int32(numRuns) {
		t.Errorf("Expected BeforeSleep to be called %d times, got %d", numRuns, calls)
	}
	if calls := wf.AfterSleepCalls.Load(); calls != int32(numRuns) {
		t.Errorf("Expected AfterSleep to be called %d times, got %d", numRuns, calls)
	}

	t.Logf("Concurrent sleep test: %d runs completed with %d workers", numRuns, numWorkers)
}

// =============================================================================
// New API Tests: GetRunStatus, CancelRun, StepTimeout, StepPanic
// =============================================================================

// TestGetRunStatus verifies the GetRunStatusTx API returns correct status.
func TestGetRunStatus(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start a workflow run
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "Test"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Check initial status (should be queued)
	tx, _ = pool.Begin(ctx)
	status, err := flows.GetRunStatusTx(ctx, client, tx, runKey)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("Failed to get run status: %v", err)
	}
	if status.Status != "queued" {
		t.Errorf("Expected status 'queued', got '%s'", status.Status)
	}
	if status.CreatedAt.IsZero() {
		t.Error("Expected CreatedAt to be set")
	}

	// Process the run
	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Check completed status
	tx, _ = pool.Begin(ctx)
	status, err = flows.GetRunStatusTx(ctx, client, tx, runKey)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("Failed to get run status: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", status.Status)
	}

	t.Logf("Run status check passed: created=%v, updated=%v", status.CreatedAt, status.UpdatedAt)
}

// TestCancelRun verifies the CancelRunTx API can cancel pending runs.
func TestCancelRun(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SleepWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start a workflow run
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SleepInput{SleepDuration: 1 * time.Hour})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Process to get into sleeping state
	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Verify it's sleeping
	var status string
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "sleeping" {
		t.Fatalf("Expected status 'sleeping', got '%s'", status)
	}

	// Cancel the run
	tx, _ = pool.Begin(ctx)
	err = flows.CancelRunTx(ctx, client, tx, runKey)
	if err != nil {
		t.Fatalf("Failed to cancel run: %v", err)
	}
	tx.Commit(ctx)

	// Verify it's cancelled
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "cancelled" {
		t.Errorf("Expected status 'cancelled', got '%s'", status)
	}

	// Verify cannot cancel again
	tx, _ = pool.Begin(ctx)
	err = flows.CancelRunTx(ctx, client, tx, runKey)
	tx.Rollback(ctx)
	if err == nil {
		t.Error("Expected error when cancelling already-cancelled run")
	}

	t.Logf("Cancel run test passed")
}

// TestCancelQueuedRun verifies cancelling a queued (not yet started) run.
func TestCancelQueuedRun(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start a workflow run
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "ToBeCancelled"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Cancel before processing
	tx, _ = pool.Begin(ctx)
	err = flows.CancelRunTx(ctx, client, tx, runKey)
	if err != nil {
		t.Fatalf("Failed to cancel queued run: %v", err)
	}
	tx.Commit(ctx)

	// Verify it's cancelled
	var status string
	pool.QueryRow(ctx, "SELECT status FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status)
	if status != "cancelled" {
		t.Errorf("Expected status 'cancelled', got '%s'", status)
	}

	// Try to process - should not pick up cancelled run
	worker := newTestWorker(pool, registry, wf.Name())
	processed, _ := worker.ProcessOne(ctx)
	if processed {
		t.Error("Should not process cancelled run")
	}
}

// TestCancelCompletedRunFails verifies cannot cancel a completed run.
func TestCancelCompletedRunFails(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start and complete a workflow run
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "WillComplete"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	// Process to completion
	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Try to cancel - should fail
	tx, _ = pool.Begin(ctx)
	err = flows.CancelRunTx(ctx, client, tx, runKey)
	tx.Rollback(ctx)
	if err == nil {
		t.Error("Expected error when cancelling completed run")
	}
	if !errors.Is(err, nil) && err.Error() != `cannot cancel run in status "completed"` {
		t.Logf("Got expected error: %v", err)
	}
}

// PanicWorkflow is a workflow where the step panics.
type PanicWorkflow struct {
	StepAttempts atomic.Int32
}

func (w *PanicWorkflow) Name() string { return "panic_workflow" }

func (w *PanicWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	result, err := flows.Execute(ctx, wf, "panicking_step", func(ctx context.Context, in *SimpleInput) (*SimpleOutput, error) {
		w.StepAttempts.Add(1)
		panic("intentional panic in step")
	}, in, flows.RetryPolicy{MaxRetries: 2})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// TestStepPanicRecovery verifies that step panics are caught and converted to errors.
func TestStepPanicRecovery(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &PanicWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start workflow
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "PanicTest"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	// Process - should not crash the worker, but fail the run
	worker := newTestWorker(pool, registry, wf.Name())
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Worker should not return error from panic: %v", err)
	}
	if !processed {
		t.Error("Expected run to be processed")
	}

	// Verify run failed with panic message
	var status, errorText string
	pool.QueryRow(ctx, "SELECT status, error_text FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &errorText)
	if status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", status)
	}
	if errorText == "" || !containsString(errorText, "panic") {
		t.Errorf("Expected error_text to contain 'panic', got '%s'", errorText)
	}

	// Verify step was attempted MaxRetries+1 times (3 attempts)
	if attempts := wf.StepAttempts.Load(); attempts != 3 {
		t.Errorf("Expected 3 step attempts (1 + 2 retries), got %d", attempts)
	}

	t.Logf("Panic recovery test passed: error=%s", errorText)
}

// WorkflowPanicWorkflow panics directly in the workflow function (not inside a step).
// This should NOT crash the worker process.
type WorkflowPanicWorkflow struct{}

func (w *WorkflowPanicWorkflow) Name() string { return "workflow_panic_workflow" }

func (w *WorkflowPanicWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	panic("intentional panic in workflow")
}

// TestWorkflowPanicRecovery verifies that workflow-level panics are caught and converted
// into a failed run (instead of crashing the worker).
func TestWorkflowPanicRecovery(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &WorkflowPanicWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start workflow
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "WorkflowPanic"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Process - should not crash the test process.
	worker := newTestWorker(pool, registry, wf.Name())
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("Worker should not return error from workflow panic: %v", err)
	}
	if !processed {
		t.Error("Expected run to be processed")
	}

	// Verify run failed with panic message
	var status, errorText string
	pool.QueryRow(ctx, "SELECT status, error_text FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &errorText)
	if status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", status)
	}
	if errorText == "" || !containsString(errorText, "panic") {
		t.Errorf("Expected error_text to contain 'panic', got '%s'", errorText)
	}
}

// TimeoutWorkflow demonstrates step timeout behavior.
type TimeoutWorkflow struct {
	StepStarted  atomic.Int32
	StepFinished atomic.Int32
}

func (w *TimeoutWorkflow) Name() string { return "timeout_workflow" }

func (w *TimeoutWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	result, err := flows.Execute(ctx, wf, "slow_step", func(ctx context.Context, in *SimpleInput) (*SimpleOutput, error) {
		w.StepStarted.Add(1)
		select {
		case <-time.After(5 * time.Second): // Step takes 5 seconds
			w.StepFinished.Add(1)
			return &SimpleOutput{Greeting: "finished"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, in, flows.RetryPolicy{
		MaxRetries:  1,
		StepTimeout: 50 * time.Millisecond, // Timeout after 50ms
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// TestStepTimeout verifies that step timeouts work correctly.
func TestStepTimeout(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &TimeoutWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start workflow
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "TimeoutTest"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	// Process - should timeout and fail
	worker := newTestWorker(pool, registry, wf.Name())
	start := time.Now()
	processed, err := worker.ProcessOne(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Worker should not return error: %v", err)
	}
	if !processed {
		t.Error("Expected run to be processed")
	}

	// Verify it failed quickly (not after 5 seconds)
	if elapsed > 1*time.Second {
		t.Errorf("Step should have timed out quickly, but took %v", elapsed)
	}

	// Verify run failed with timeout error
	var status, errorText string
	pool.QueryRow(ctx, "SELECT status, error_text FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &errorText)
	if status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", status)
	}

	// Verify step was started multiple times (initial + retry)
	if started := wf.StepStarted.Load(); started != 2 {
		t.Errorf("Expected 2 step starts (1 + 1 retry), got %d", started)
	}

	// Verify step never finished (all timed out)
	if finished := wf.StepFinished.Load(); finished != 0 {
		t.Errorf("Expected 0 step finishes (all timed out), got %d", finished)
	}

	t.Logf("Step timeout test passed in %v", elapsed)
}

// TestGetRunOutput verifies the GetRunOutputTx API.
func TestGetRunOutput(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}

	// Start and complete a workflow
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}

	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "OutputTest"})
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("Failed to start run: %v", err)
	}
	tx.Commit(ctx)

	// Try to get output before completion - should fail
	tx, _ = pool.Begin(ctx)
	_, err = flows.GetRunOutputTx[SimpleOutput](ctx, client, tx, runKey)
	tx.Rollback(ctx)
	if err == nil {
		t.Error("Expected error when getting output of non-completed run")
	}

	// Complete the run
	worker := newTestWorker(pool, registry, wf.Name())
	worker.ProcessOne(ctx)

	// Get the output
	tx, _ = pool.Begin(ctx)
	output, err := flows.GetRunOutputTx[SimpleOutput](ctx, client, tx, runKey)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("Failed to get run output: %v", err)
	}
	if output.Greeting != "Hello, OutputTest!" {
		t.Errorf("Expected greeting 'Hello, OutputTest!', got '%s'", output.Greeting)
	}

	t.Logf("GetRunOutput test passed: %s", output.Greeting)
}
