package flows

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Worker polls Postgres for runnable workflow runs and executes them.
//
// A run is runnable when:
// - status = 'queued', or
// - status = 'sleeping' and next_wake_at <= now().
//
// Each registered workflow type runs in its own goroutine pool with configurable
// concurrency. This ensures busy workflows don't block other workflow types.
//
// # Citus Compatibility
//
// The runs table is distributed by workflow_name_shard (the distribution column).
// Citus requires SELECT ... FOR UPDATE SKIP LOCKED to be routed to a single shard,
// which means the query must include an equality predicate on workflow_name_shard.
//
// Each workflow's shards are derived from: workflow_name + "_" + shard_index
// (e.g., "my_workflow_0", "my_workflow_1", ...).
//
// The worker iterates through all shards for each workflow, using round-robin
// rotation to prevent starvation when some shards have more work than others.
type Worker struct {
	Pool         *pgxpool.Pool
	Registry     *Registry
	Codec        Codec
	PollInterval time.Duration
	DBConfig     DBConfig

	// DisableNotify disables LISTEN/NOTIFY wakeups. Polling remains enabled.
	DisableNotify bool

	// NotifyChannel overrides the Postgres channel name used for LISTEN/NOTIFY.
	// If empty, a safe default is used.
	NotifyChannel string

	// GracefulShutdownTimeout is the maximum time to wait for in-progress runs
	// to complete when the worker is shutting down. If zero, the worker will
	// wait indefinitely for all in-progress work to complete.
	GracefulShutdownTimeout time.Duration
}

// workflowState tracks per-workflow state for shard rotation.
type workflowState struct {
	shards  []string // all shard values for this workflow
	shardRR uint64   // round-robin counter for shard rotation
}

func (w *Worker) pollInterval() time.Duration {
	if w.PollInterval <= 0 {
		return 250 * time.Millisecond
	}
	return w.PollInterval
}

func (w *Worker) notifyChannel() string {
	return normalizeNotifyChannel(w.NotifyChannel)
}

// Run starts the worker and blocks until ctx is cancelled.
// Each registered workflow type runs in its own goroutine pool based on its
// configured concurrency. This ensures busy workflows don't block other types.
//
// Within each workflow's pool, workers iterate through all shards (workflow_name_shard values)
// to find work. This is required for Citus compatibility where FOR UPDATE SKIP LOCKED
// must be routed to a single shard.
func (w *Worker) Run(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("flows: Worker.Pool is required")
	}
	if w.Registry == nil {
		return errors.New("flows: Worker.Registry is required")
	}

	workflows := w.Registry.list()
	if len(workflows) == 0 {
		return errors.New("flows: no workflows registered")
	}

	shardCount := w.DBConfig.ShardCount
	if shardCount <= 0 {
		shardCount = 1
	}

	// Set up LISTEN/NOTIFY for low-latency wakeups
	notifyCh := make(chan struct{}, 1)
	if !w.DisableNotify {
		go w.listenLoop(ctx, notifyCh)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	// Start a goroutine pool for each workflow type
	for _, runner := range workflows {
		concurrency := runner.concurrency()
		workflowName := runner.workflowName()

		// Create shared state for this workflow's goroutines
		state := &workflowState{
			shards: ShardValuesForWorkflow(workflowName, shardCount),
		}

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(name string, st *workflowState) {
				defer wg.Done()
				if err := w.runWorkflowLoop(ctx, name, st, notifyCh); err != nil {
					select {
					case errCh <- err:
					default:
					}
				}
			}(workflowName, state)
		}
	}

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		// Graceful shutdown: wait for in-progress work to complete
		if w.GracefulShutdownTimeout > 0 {
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				// All workers finished gracefully
			case <-time.After(w.GracefulShutdownTimeout):
				// Timeout waiting for workers; they will be cancelled
			}
		} else {
			// No timeout: wait indefinitely for all work to complete
			wg.Wait()
		}
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// listenLoop maintains a LISTEN connection and signals notifyCh on notifications.
func (w *Worker) listenLoop(ctx context.Context, notifyCh chan<- struct{}) {
	for ctx.Err() == nil {
		conn, err := w.Pool.Acquire(ctx)
		if err != nil {
			time.Sleep(w.pollInterval())
			continue
		}

		ch := w.notifyChannel()
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			conn.Release()
			time.Sleep(w.pollInterval())
			continue
		}

		pgxConn := conn.Conn()
		for {
			_, err := pgxConn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			// Non-blocking send to wake up workers
			select {
			case notifyCh <- struct{}{}:
			default:
			}
		}

		conn.Release()
		if ctx.Err() != nil {
			return
		}
	}
}

// runWorkflowLoop processes runs for a specific workflow type.
// It iterates through all shards to find work, using round-robin rotation
// to prevent starvation when some shards have more work.
func (w *Worker) runWorkflowLoop(ctx context.Context, workflowName string, state *workflowState, notifyCh <-chan struct{}) error {
	for {
		processed, err := w.processOneForWorkflow(ctx, workflowName, state)
		if err != nil {
			return err
		}
		if processed {
			continue
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Wait for notification or poll interval
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notifyCh:
		case <-time.After(w.pollInterval()):
		}
	}
}

// processOneForWorkflow claims and executes one run for a specific workflow type.
// It iterates through all shards for the workflow, starting from a rotating position
// to ensure fair distribution of work across shards.
//
// On Citus, the query must include workflow_name_shard in the WHERE clause
// so that FOR UPDATE SKIP LOCKED is routed to a single shard.
func (w *Worker) processOneForWorkflow(ctx context.Context, workflowName string, state *workflowState) (processed bool, err error) {
	shards := state.shards
	if len(shards) == 0 {
		return false, nil
	}

	// Single shard - no rotation needed
	if len(shards) == 1 {
		return w.processOneShard(ctx, workflowName, shards[0])
	}

	// Multiple shards - rotate starting position to prevent starvation
	start := int(atomic.AddUint64(&state.shardRR, 1) % uint64(len(shards)))
	for i := 0; i < len(shards); i++ {
		shard := shards[(start+i)%len(shards)]
		processed, err = w.processOneShard(ctx, workflowName, shard)
		if err != nil || processed {
			return processed, err
		}
	}
	return false, nil
}

// processOneShard claims and executes one run from a specific shard.
// The shard value (workflow_name_shard) is included in the WHERE clause
// to ensure Citus routes the query to a single node.
func (w *Worker) processOneShard(ctx context.Context, workflowName, shard string) (processed bool, err error) {
	t := newDBTables(w.DBConfig)

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	var runID string
	var inputJSON []byte

	// Query with workflow_name_shard equality predicate for Citus compatibility.
	// This ensures FOR UPDATE SKIP LOCKED is routed to a single shard.
	err = tx.QueryRow(ctx, t.claimRunnableRunForShardSQL(), runStatusQueued, runStatusSleeping, shard, workflowName).
		Scan(&runID, &inputJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return false, nil
		}

		// If the schema/tables haven't been created yet, don't crash the worker.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "3F000" || pgErr.Code == "42P01" {
				_ = tx.Rollback(ctx)
				return false, nil
			}
		}
		return false, err
	}

	_, err = tx.Exec(ctx, t.setRunRunningSQL(), shard, runID, runStatusRunning)
	if err != nil {
		return false, err
	}

	runner, ok := w.Registry.get(workflowName)
	if !ok {
		_, _ = tx.Exec(ctx, t.setRunFailedSQL(), shard, runID, runStatusFailed, "workflow not registered: "+workflowName)
		err = tx.Commit(ctx)
		return true, err
	}

	wfCtx := newContext(RunKey{WorkflowNameShard: shard, RunID: RunID(runID)}, tx, runner.codec(), t)

	// Run the workflow, catching durable yields.
	var outputJSON []byte
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				if y, ok := r.(yieldPanic); ok {
					runErr = y
					return
				}
				buf := make([]byte, 64*1024)
				n := runtime.Stack(buf, false)
				runErr = WorkflowPanicError{Value: r, Stack: string(buf[:n])}
			}
		}()

		outputJSON, runErr = runner.run(ctx, wfCtx, inputJSON)
	}()

	if runErr != nil {
		if _, ok := runErr.(yieldPanic); ok {
			err = tx.Commit(ctx)
			return true, err
		}

		_, _ = tx.Exec(ctx, t.setRunFailedSQL(), shard, runID, runStatusFailed, runErr.Error())
		err = tx.Commit(ctx)
		return true, err
	}

	_, err = tx.Exec(ctx, t.setRunCompletedSQL(), shard, runID, runStatusCompleted, outputJSON)
	if err != nil {
		return false, fmt.Errorf("persist run output: %w", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		return false, fmt.Errorf("commit workflow run %s: %w", runID, err)
	}
	return true, nil
}

// ProcessOne claims and executes at most one runnable run from any registered workflow.
// Returns processed=false if no runnable runs exist.
// This is useful for testing or when fine-grained control over processing is needed.
func (w *Worker) ProcessOne(ctx context.Context) (processed bool, err error) {
	shardCount := w.DBConfig.ShardCount
	if shardCount <= 0 {
		shardCount = 1
	}

	workflows := w.Registry.list()
	for _, runner := range workflows {
		workflowName := runner.workflowName()
		shards := ShardValuesForWorkflow(workflowName, shardCount)
		for _, shard := range shards {
			processed, err = w.processOneShard(ctx, workflowName, shard)
			if err != nil || processed {
				return processed, err
			}
		}
	}
	return false, nil
}
