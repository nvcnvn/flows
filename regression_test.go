package flows_test

// Regression tests for gaps found in the durability/liveness review:
//
//  1. Lost-wakeup race: an event published while the run's transaction is
//     still executing (committed status not yet waiting_event) must not
//     strand the run in waiting_event forever.
//  2. Aborted workflow transactions must mark the run failed (in a fresh
//     transaction) instead of crash-looping every worker that claims it.
//  3. A NOTIFY wakeup must reach every workflow dispatcher, not just one.
//  4. A schedule row with an unparseable cron expression must be disabled,
//     not stall the whole cron loop; ScheduleTx must reject schedules that
//     don't round-trip through ParseCron.
//  5. Graceful shutdown must let in-flight runs finish within
//     GracefulShutdownTimeout instead of failing their final writes.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	flows "github.com/nvcnvn/flows"
)

// TestEventArrivedWhileRunning_RunIsNotStranded reproduces the lost-wakeup
// race deterministically. The publisher's wake UPDATE only matches runs whose
// committed status is waiting_event; if the publisher commits while the run's
// transaction is mid-flight, the wake is a no-op. The claim query must still
// pick up such runs because an unsatisfied event wait has a matching event.
func TestEventArrivedWhileRunning_RunIsNotStranded(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	registry := flows.NewRegistry()
	wf := &EventWorkflow{}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	runKey, err := flows.BeginTx(ctx, client, tx, wf, &EventInput{EventName: "approval"})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	worker := newTestWorker(pool, registry)

	// First pass: workflow runs until WaitForEvent and yields (waiting_event).
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("ProcessOne #1: processed=%v err=%v", processed, err)
	}
	waitForRunStatus(t, pool, runKey, "waiting_event", 2*time.Second)

	// Simulate the racing publisher: the event row is committed, but the
	// wait was not yet visible so neither the satisfy UPDATE nor the wake
	// UPDATE matched anything.
	_, err = pool.Exec(ctx,
		"INSERT INTO flows.events (workflow_name_shard, run_id, event_name, payload_json) VALUES ($1, $2, $3, $4)",
		runKey.WorkflowNameShard, string(runKey.RunID), "approval", []byte(`{"data":"raced"}`),
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// The run must still be claimable and complete using the stored event.
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne #2: %v", err)
	}
	if !processed {
		t.Fatal("run with an already-arrived event was not claimed; it is stranded in waiting_event")
	}
	waitForRunStatus(t, pool, runKey, "completed", 2*time.Second)

	out, err := flows.GetRunOutputTx[EventOutput](ctx, client, pool, runKey)
	if err != nil {
		t.Fatalf("GetRunOutputTx: %v", err)
	}
	if out.ReceivedData != "raced" {
		t.Errorf("expected payload %q, got %q", "raced", out.ReceivedData)
	}
}

// PoisonTxWorkflow aborts its own workflow transaction with a failing SQL
// statement, then fails. Before the fix, the worker could neither record the
// failure (aborted tx) nor survive: the commit error killed the whole worker,
// the run rolled back to queued, and the next worker died the same way.
type PoisonTxWorkflow struct {
	Claims atomic.Int32
}

func (w *PoisonTxWorkflow) Name() string { return "poison_tx_workflow" }

func (w *PoisonTxWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	w.Claims.Add(1)
	return flows.Execute(ctx, wf, "bad_sql", func(ctx context.Context, in *SimpleInput) (*SimpleOutput, error) {
		_, err := wf.Tx().Exec(ctx, "INSERT INTO this_table_does_not_exist VALUES (1)")
		if err == nil {
			return nil, errors.New("expected SQL failure")
		}
		return nil, err
	}, in, flows.RetryPolicy{})
}

// TestWorkerSurvivesAbortedWorkflowTransaction verifies that a run which
// aborts its transaction is marked failed and the worker keeps processing
// other runs.
func TestWorkerSurvivesAbortedWorkflowTransaction(t *testing.T) {
	pool := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := flows.NewRegistry()
	poison := &PoisonTxWorkflow{}
	healthy := &SimpleWorkflow{}
	flows.Register(registry, poison)
	flows.Register(registry, healthy)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	poisonKey, err := flows.BeginTx(ctx, client, tx, poison, &SimpleInput{Name: "boom"})
	if err != nil {
		t.Fatalf("BeginTx poison: %v", err)
	}
	healthyKey, err := flows.BeginTx(ctx, client, tx, healthy, &SimpleInput{Name: "ok"})
	if err != nil {
		t.Fatalf("BeginTx healthy: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	worker := &flows.Worker{
		Pool:         pool,
		Registry:     registry,
		PollInterval: 50 * time.Millisecond,
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()

	// The poison run must end up failed (recorded via a fresh transaction),
	// and the healthy run must complete: the worker survived.
	waitForRunStatus(t, pool, poisonKey, "failed", 10*time.Second)
	waitForRunStatus(t, pool, healthyKey, "completed", 10*time.Second)

	// The poison run must not be claimed over and over.
	claimsAfterFailure := poison.Claims.Load()
	time.Sleep(300 * time.Millisecond)
	if again := poison.Claims.Load(); again != claimsAfterFailure {
		t.Errorf("failed run was re-claimed: claims went from %d to %d", claimsAfterFailure, again)
	}

	var errorText string
	err = pool.QueryRow(ctx,
		"SELECT error_text FROM flows.runs WHERE workflow_name_shard = $1 AND run_id = $2",
		poisonKey.WorkflowNameShard, string(poisonKey.RunID),
	).Scan(&errorText)
	if err != nil {
		t.Fatalf("query error_text: %v", err)
	}
	if errorText == "" {
		t.Error("expected error_text to be recorded for the failed run")
	}

	cancel()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down")
	}
}

// secondWorkflow is a distinct workflow type so the worker runs two dispatchers.
type secondWorkflow struct{ SimpleWorkflow }

func (w *secondWorkflow) Name() string { return "second_workflow" }

// TestNotifyWakesAllDispatchers verifies a single NOTIFY burst wakes the
// dispatchers of all registered workflow types. Before the fix, one shared
// channel meant only one (arbitrary) dispatcher woke up and the others slept
// until their poll backoff expired.
func TestNotifyWakesAllDispatchers(t *testing.T) {
	pool := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := flows.NewRegistry()
	wfA := &SimpleWorkflow{}
	wfB := &secondWorkflow{}
	flows.Register(registry, wfA)
	flows.Register(registry, wfB)

	worker := &flows.Worker{
		Pool:     pool,
		Registry: registry,
		// Long poll interval: completion within the assertion window is only
		// possible via LISTEN/NOTIFY wakeups.
		PollInterval: 10 * time.Second,
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()

	// Give dispatchers time to do their initial empty poll and the listener
	// time to establish LISTEN.
	time.Sleep(1 * time.Second)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	keyA, err := flows.BeginTx(ctx, client, tx, wfA, &SimpleInput{Name: "A"})
	if err != nil {
		t.Fatalf("BeginTx A: %v", err)
	}
	keyB, err := flows.BeginTx(ctx, client, tx, wfB, &SimpleInput{Name: "B"})
	if err != nil {
		t.Fatalf("BeginTx B: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Both runs must complete well before the 10s poll interval.
	waitForRunStatus(t, pool, keyA, "completed", 3*time.Second)
	waitForRunStatus(t, pool, keyB, "completed", 3*time.Second)

	cancel()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down")
	}
}

// TestInvalidCronExpressionIsDisabledNotStalling verifies that a schedule row
// with an unparseable cron expression is disabled by the worker and does not
// starve other due schedules.
func TestInvalidCronExpressionIsDisabledNotStalling(t *testing.T) {
	pool := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := flows.NewRegistry()
	wf := &SimpleWorkflow{}
	flows.Register(registry, wf)

	// A poison schedule that is due earlier than everything else.
	_, err := pool.Exec(ctx, `
		INSERT INTO flows.schedules (schedule_id, workflow_name, cron_expr, input_json, enabled, next_run_at)
		VALUES ('poison', 'simple_workflow', 'definitely not cron', '{}', true, now() - interval '1 minute')
	`)
	if err != nil {
		t.Fatalf("insert poison schedule: %v", err)
	}

	// A valid schedule behind it.
	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := flows.ScheduleTx(ctx, client, tx, wf, &SimpleInput{Name: "cron"}, "good", flows.Every(500*time.Millisecond)); err != nil {
		t.Fatalf("ScheduleTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	worker := &flows.Worker{
		Pool:         pool,
		Registry:     registry,
		PollInterval: 50 * time.Millisecond,
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()

	// The poison schedule must be disabled, and the good schedule must still fire.
	deadline := time.Now().Add(10 * time.Second)
	var poisonEnabled = true
	var goodRuns int
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, "SELECT enabled FROM flows.schedules WHERE schedule_id = 'poison'").Scan(&poisonEnabled)
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM flows.runs WHERE workflow_name = 'simple_workflow'").Scan(&goodRuns)
		if !poisonEnabled && goodRuns >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if poisonEnabled {
		t.Error("poison schedule was not disabled; it would stall the cron loop forever")
	}
	if goodRuns < 1 {
		t.Error("valid schedule behind the poison schedule never fired")
	}

	cancel()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down")
	}
}

// opaqueSchedule is a valid Schedule implementation whose textual form is not
// a parseable cron expression.
type opaqueSchedule struct{}

func (opaqueSchedule) Next(after time.Time) time.Time { return after.Add(time.Hour) }

// TestScheduleTxRejectsNonRoundTrippableSchedule verifies ScheduleTx fails
// fast for custom Schedule implementations that cannot be re-parsed by the
// worker, instead of persisting a poison row.
func TestScheduleTxRejectsNonRoundTrippableSchedule(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	wf := &SimpleWorkflow{}
	client := flows.Client{}

	err := flows.ScheduleTx(ctx, client, pool, wf, &SimpleInput{}, "opaque", opaqueSchedule{})
	if err == nil {
		t.Fatal("expected ScheduleTx to reject a schedule that does not round-trip through ParseCron")
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM flows.schedules").Scan(&count); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no schedule rows to be persisted, found %d", count)
	}
}

// slowStepWorkflow signals when its step starts, then takes a while.
type slowStepWorkflow struct {
	Started  chan struct{}
	Duration time.Duration
}

func (w *slowStepWorkflow) Name() string { return "slow_step_workflow" }

func (w *slowStepWorkflow) Run(ctx context.Context, wf *flows.Context, in *SimpleInput) (*SimpleOutput, error) {
	return flows.Execute(ctx, wf, "slow", func(ctx context.Context, in *SimpleInput) (*SimpleOutput, error) {
		select {
		case w.Started <- struct{}{}:
		default:
		}
		time.Sleep(w.Duration)
		return &SimpleOutput{Greeting: "done"}, nil
	}, in, flows.RetryPolicy{})
}

// TestGracefulShutdownCompletesInFlightRun verifies that cancelling the
// worker context while a run is executing lets the run finish and commit
// within GracefulShutdownTimeout, instead of failing its final writes with a
// cancelled context and rolling everything back.
func TestGracefulShutdownCompletesInFlightRun(t *testing.T) {
	pool := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := flows.NewRegistry()
	wf := &slowStepWorkflow{Started: make(chan struct{}, 1), Duration: 700 * time.Millisecond}
	flows.Register(registry, wf)

	client := flows.Client{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	runKey, err := flows.BeginTx(ctx, client, tx, wf, &SimpleInput{Name: "slow"})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	worker := &flows.Worker{
		Pool:                    pool,
		Registry:                registry,
		PollInterval:            50 * time.Millisecond,
		GracefulShutdownTimeout: 10 * time.Second,
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()

	select {
	case <-wf.Started:
	case <-time.After(5 * time.Second):
		t.Fatal("run never started")
	}

	// Shut down while the step is still executing.
	cancel()

	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down within the grace period")
	}

	// The in-flight run must have committed its result.
	waitForRunStatus(t, pool, runKey, "completed", 2*time.Second)
}
