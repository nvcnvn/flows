package flows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
// Each registered workflow type uses one dispatcher goroutine plus a bounded
// executor pool with configurable concurrency. This ensures busy workflows
// don't block other workflow types while avoiding duplicated DB polling.
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
	// MaxPollInterval caps adaptive backoff after consecutive empty polls.
	// If zero, workers back off up to 30s when LISTEN/NOTIFY is enabled.
	// When notifications are disabled, polling stays at PollInterval.
	MaxPollInterval time.Duration
	DBConfig        DBConfig

	// DisableNotify disables LISTEN/NOTIFY wakeups. Polling remains enabled.
	DisableNotify bool

	// NotifyChannel overrides the Postgres channel name used for LISTEN/NOTIFY.
	// If empty, a safe default is used.
	NotifyChannel string

	// GracefulShutdownTimeout is the maximum time to wait for in-progress runs
	// to complete when the worker is shutting down. If zero, the worker will
	// wait indefinitely for all in-progress work to complete.
	GracefulShutdownTimeout time.Duration

	// Logger receives internal worker errors that would otherwise be silent
	// (failed runs, claim errors, listen failures, disabled schedules).
	// If nil, these errors are dropped.
	Logger *slog.Logger

	claimOneForWorkflowFunc func(ctx context.Context, runner workflowRunner, state *workflowState) (job claimedRun, claimed bool, err error)
	onClaimAttempt          func(workflowName string)
	disableCronLoop         bool
}

// workflowState tracks per-workflow state for shard rotation.
type workflowState struct {
	shards  []string // all shard values for this workflow
	shardRR uint64   // round-robin counter for shard rotation
}

type claimedRun struct {
	runner    workflowRunner
	shard     string
	runID     string
	inputJSON []byte
	tx        pgx.Tx
}

type idleBackoff struct {
	base    time.Duration
	max     time.Duration
	current time.Duration
}

func newIdleBackoff(base, max time.Duration) idleBackoff {
	if base <= 0 {
		base = 250 * time.Millisecond
	}
	if max < base {
		max = base
	}
	return idleBackoff{base: base, max: max, current: base}
}

func (b *idleBackoff) next() time.Duration {
	wait := b.current
	if wait <= 0 {
		wait = b.base
	}
	if b.current < b.max {
		b.current *= 2
		if b.current > b.max {
			b.current = b.max
		}
	}
	return wait
}

func (b *idleBackoff) reset() {
	b.current = b.base
}

func (w *Worker) pollInterval() time.Duration {
	if w.PollInterval <= 0 {
		return 250 * time.Millisecond
	}
	return w.PollInterval
}

func (w *Worker) maxPollInterval() time.Duration {
	base := w.pollInterval()
	if w.DisableNotify {
		return base
	}
	if w.MaxPollInterval <= 0 {
		if base > 30*time.Second {
			return base
		}
		return 30 * time.Second
	}
	if w.MaxPollInterval < base {
		return base
	}
	return w.MaxPollInterval
}

func (w *Worker) notifyChannel() string {
	return normalizeNotifyChannel(w.NotifyChannel)
}

// logError reports an internal worker error via the configured Logger.
// It is a no-op when no Logger is set.
func (w *Worker) logError(msg string, args ...any) {
	if w.Logger != nil {
		w.Logger.Error(msg, args...)
	}
}

// Run starts the worker and blocks until ctx is cancelled.
// Each registered workflow type gets one dispatcher goroutine that claims work
// from Postgres and a bounded in-process executor pool that runs claimed work.
// This preserves per-workflow concurrency without multiplying DB polling.
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

	// runCtx governs claiming/polling; cancelling it stops new work.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	// execCtx governs in-flight workflow execution. It deliberately survives
	// ctx cancellation so graceful shutdown can let claimed runs finish; it is
	// cancelled once the shutdown grace period expires.
	execCtx, cancelExec := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExec()

	// Set up LISTEN/NOTIFY for low-latency wakeups.
	runNotifyCh := make(chan struct{}, 1)
	scheduleNotifyCh := make(chan struct{}, 1)
	if !w.DisableNotify {
		go w.listenLoop(runCtx, runNotifyCh, scheduleNotifyCh)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	// Start one dispatcher plus an executor pool for each workflow type.
	// Each dispatcher gets its own notify channel; a single notification must
	// wake every dispatcher, not just whichever one happens to receive first.
	dispatcherNotifyChs := make([]chan struct{}, 0, len(workflows))
	for _, runner := range workflows {
		concurrency := runner.concurrency()

		// Create shared state for this workflow's dispatcher.
		state := &workflowState{
			shards: ShardValuesForWorkflow(runner.workflowName(), shardCount),
		}
		notifyCh := make(chan struct{}, 1)
		dispatcherNotifyChs = append(dispatcherNotifyChs, notifyCh)
		jobCh := make(chan claimedRun, concurrency)
		slots := make(chan struct{}, concurrency)
		for i := 0; i < concurrency; i++ {
			slots <- struct{}{}
		}

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.runWorkflowExecutor(execCtx, jobCh, slots)
			}()
		}

		wg.Add(1)
		go func(r workflowRunner, st *workflowState, notifyCh <-chan struct{}) {
			defer wg.Done()
			defer close(jobCh)
			if err := w.runWorkflowDispatcher(runCtx, r, st, notifyCh, jobCh, slots); err != nil && runCtx.Err() == nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}(runner, state, notifyCh)
	}

	// Fan a run notification out to every dispatcher.
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-runNotifyCh:
				for _, ch := range dispatcherNotifyChs {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	// Start cron loop unconditionally. It is a no-op when the schedules table
	// is empty, and picks up schedules created at runtime via ScheduleTx.
	if !w.disableCronLoop {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.runCronLoop(runCtx, scheduleNotifyCh); err != nil && runCtx.Err() == nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}

	// Wait for context cancellation or a fatal error.
	var fatalErr error
	select {
	case <-ctx.Done():
		fatalErr = ctx.Err()
	case fatalErr = <-errCh:
	}

	// Stop claiming new work; dispatchers exit and close their job channels.
	cancelRun()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	if w.GracefulShutdownTimeout > 0 {
		select {
		case <-done:
			// All in-progress work finished gracefully.
		case <-time.After(w.GracefulShutdownTimeout):
			// Grace period expired: cancel in-flight executions and wait for
			// goroutines to unwind so we don't leak them.
			cancelExec()
			<-done
		}
	} else {
		// No timeout: wait indefinitely for all in-progress work to complete.
		<-done
	}
	return fatalErr
}

// listenLoop maintains a LISTEN connection and routes notifications by payload.
func (w *Worker) listenLoop(ctx context.Context, runNotifyCh chan<- struct{}, scheduleNotifyCh chan<- struct{}) {
	retryWait := func() {
		select {
		case <-ctx.Done():
		case <-time.After(w.pollInterval()):
		}
	}
	for ctx.Err() == nil {
		conn, err := w.Pool.Acquire(ctx)
		if err != nil {
			retryWait()
			continue
		}

		ch := w.notifyChannel()
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			conn.Release()
			if ctx.Err() == nil {
				w.logError("flows: LISTEN failed", "channel", ch, "error", err)
			}
			retryWait()
			continue
		}

		pgxConn := conn.Conn()
		for {
			notification, err := pgxConn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			if notification != nil && notification.Payload == notifyPayloadScheduleRefresh {
				select {
				case scheduleNotifyCh <- struct{}{}:
				default:
				}
				continue
			}
			select {
			case runNotifyCh <- struct{}{}:
			default:
			}
		}

		conn.Release()
		if ctx.Err() != nil {
			return
		}
	}
}

// runWorkflowDispatcher claims runs for a specific workflow type and hands them
// to the executor pool. Only this loop polls Postgres for that workflow.
func (w *Worker) runWorkflowDispatcher(ctx context.Context, runner workflowRunner, state *workflowState, notifyCh <-chan struct{}, jobCh chan<- claimedRun, slots chan struct{}) error {
	idle := newIdleBackoff(w.pollInterval(), w.maxPollInterval())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-slots:
		}

		job, claimed, err := w.claimOneForWorkflow(ctx, runner, state)
		if err != nil {
			slots <- struct{}{}
			// Context cancellation is the only reason to stop the loop.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.logError("flows: claim run", "workflow", runner.workflowName(), "error", err)
			// Transient error: back off and retry.
			wait := idle.next()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if claimed {
			idle.reset()
			select {
			case <-ctx.Done():
				// ctx is already cancelled; use a detached context so the
				// rollback still reaches the server and releases the row lock.
				_ = job.tx.Rollback(context.WithoutCancel(ctx))
				slots <- struct{}{}
				return ctx.Err()
			case jobCh <- job:
			}
			continue
		}
		slots <- struct{}{}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Wait for notification or an adaptive poll interval.
		wait := idle.next()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notifyCh:
			idle.reset()
		case <-time.After(wait):
		}
	}
}

// runWorkflowExecutor executes claimed runs for a specific workflow type.
// Per-run errors are logged, never fatal: a single bad run (or a transient
// DB error while persisting its outcome) must not take the worker down.
func (w *Worker) runWorkflowExecutor(ctx context.Context, jobCh <-chan claimedRun, slots chan<- struct{}) {
	for job := range jobCh {
		err := w.executeClaimedRun(ctx, job)
		slots <- struct{}{}
		if err != nil && ctx.Err() == nil {
			w.logError("flows: execute claimed run",
				"workflow", job.runner.workflowName(), "run_id", job.runID, "error", err)
		}
	}
}

// claimOneForWorkflow claims at most one run for a specific workflow type.
// It iterates through all shards for the workflow, starting from a rotating position
// to ensure fair distribution of work across shards.
//
// On Citus, the query must include workflow_name_shard in the WHERE clause
// so that FOR UPDATE SKIP LOCKED is routed to a single shard.
func (w *Worker) claimOneForWorkflow(ctx context.Context, runner workflowRunner, state *workflowState) (job claimedRun, claimed bool, err error) {
	if w.onClaimAttempt != nil {
		w.onClaimAttempt(runner.workflowName())
	}
	if w.claimOneForWorkflowFunc != nil {
		return w.claimOneForWorkflowFunc(ctx, runner, state)
	}

	workflowName := runner.workflowName()
	shards := state.shards
	if len(shards) == 0 {
		return claimedRun{}, false, nil
	}

	// Single shard - no rotation needed
	if len(shards) == 1 {
		return w.claimOneShard(ctx, runner, workflowName, shards[0])
	}

	// Multiple shards - rotate starting position to prevent starvation
	start := int(atomic.AddUint64(&state.shardRR, 1) % uint64(len(shards)))
	for i := 0; i < len(shards); i++ {
		shard := shards[(start+i)%len(shards)]
		job, claimed, err = w.claimOneShard(ctx, runner, workflowName, shard)
		if err != nil || claimed {
			return job, claimed, err
		}
	}
	return claimedRun{}, false, nil
}

// claimOneShard claims one run from a specific shard and marks it running.
// The shard value (workflow_name_shard) is included in the WHERE clause
// to ensure Citus routes the query to a single node.
func (w *Worker) claimOneShard(ctx context.Context, runner workflowRunner, workflowName, shard string) (job claimedRun, claimed bool, err error) {
	t := newDBTables(w.DBConfig)

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return claimedRun{}, false, err
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
	err = tx.QueryRow(ctx, t.claimRunnableRunForShardSQL(), runStatusQueued, runStatusSleeping, shard, workflowName, runStatusWaitingEvent, waitTypeEvent).
		Scan(&runID, &inputJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return claimedRun{}, false, nil
		}

		// If the schema/tables haven't been created yet, don't crash the worker.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "3F000" || pgErr.Code == "42P01" {
				_ = tx.Rollback(ctx)
				return claimedRun{}, false, nil
			}
		}
		return claimedRun{}, false, err
	}

	_, err = tx.Exec(ctx, t.setRunRunningSQL(), shard, runID, runStatusRunning)
	if err != nil {
		return claimedRun{}, false, err
	}

	return claimedRun{
		runner:    runner,
		shard:     shard,
		runID:     runID,
		inputJSON: inputJSON,
		tx:        tx,
	}, true, nil
}

func (w *Worker) executeClaimedRun(ctx context.Context, job claimedRun) (err error) {
	t := newDBTables(w.DBConfig)
	tx := job.tx
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	wfCtx := newContext(RunKey{WorkflowNameShard: job.shard, RunID: RunID(job.runID)}, tx, job.runner.codec(), t)

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

		outputJSON, runErr = job.runner.run(ctx, wfCtx, job.inputJSON)
	}()

	if runErr != nil {
		if _, ok := runErr.(yieldPanic); ok {
			err = tx.Commit(ctx)
			return err
		}

		// If the worker is shutting down, don't mark the run failed: roll back
		// so it stays queued and another worker (or a restart) picks it up.
		if ctx.Err() != nil {
			err = ctx.Err()
			return err
		}

		// Terminal failure: prefer recording it inside the workflow
		// transaction so memoized steps and business writes commit with it.
		_, execErr := tx.Exec(ctx, t.setRunFailedSQL(), job.shard, job.runID, runStatusFailed, runErr.Error())
		var commitErr error
		if execErr == nil {
			commitErr = tx.Commit(ctx)
		}
		if execErr != nil || commitErr != nil {
			// The workflow transaction is unusable — typically aborted by a
			// failed SQL statement inside the workflow. Roll it back and
			// record the failure in a fresh transaction; otherwise the run
			// stays queued and every worker that claims it fails the same way.
			_ = tx.Rollback(ctx)
			if _, ferr := w.Pool.Exec(ctx, t.setRunFailedSQL(), job.shard, job.runID, runStatusFailed, runErr.Error()); ferr != nil {
				err = fmt.Errorf("mark run %s failed after tx error: %w", job.runID, ferr)
				return err
			}
		}
		return nil
	}

	_, err = tx.Exec(ctx, t.setRunCompletedSQL(), job.shard, job.runID, runStatusCompleted, outputJSON)
	if err != nil {
		return fmt.Errorf("persist run output: %w", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		return fmt.Errorf("commit workflow run %s: %w", job.runID, err)
	}
	return nil
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
		state := &workflowState{shards: ShardValuesForWorkflow(runner.workflowName(), shardCount)}
		job, claimed, err := w.claimOneForWorkflow(ctx, runner, state)
		if err != nil {
			return false, err
		}
		if claimed {
			if err := w.executeClaimedRun(ctx, job); err != nil {
				return true, err
			}
			return true, nil
		}
	}
	return false, nil
}

// -- Cron schedule support ----------------------------------------------------

// runCronLoop sleeps until the next due schedule and wakes early on schedule changes.
func (w *Worker) runCronLoop(ctx context.Context, scheduleNotifyCh <-chan struct{}) error {
	for {
		created, err := w.processOneCronSchedule(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Transient error: back off and retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.pollInterval()):
			}
			continue
		}
		if created {
			// Processed one; check for more immediately.
			continue
		}

		nextRunAt, err := w.nextScheduleDueAt(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.pollInterval()):
			}
			continue
		}

		wait := w.pollInterval()
		notifyCh := scheduleNotifyCh

		if nextRunAt == nil {
			if notifyCh == nil || w.DisableNotify {
				notifyCh = nil
			} else {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-notifyCh:
				}
				continue
			}
		} else {
			untilNext := time.Until(*nextRunAt)
			if untilNext < 0 {
				untilNext = 0
			}
			if notifyCh != nil && !w.DisableNotify {
				wait = untilNext
			} else if untilNext < wait {
				wait = untilNext
				notifyCh = nil
			} else {
				notifyCh = nil
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-notifyCh:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

// processOneCronSchedule claims one due schedule, creates a run, and advances next_run_at.
func (w *Worker) processOneCronSchedule(ctx context.Context) (bool, error) {
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

	var scheduleID, workflowName, cronExpr string
	var inputJSON []byte

	err = tx.QueryRow(ctx, t.claimDueScheduleSQL()).Scan(&scheduleID, &workflowName, &cronExpr, &inputJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return false, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "3F000" || pgErr.Code == "42P01" {
				_ = tx.Rollback(ctx)
				return false, nil
			}
		}
		return false, err
	}

	// Parse the schedule from the stored cron expression.
	sched, parseErr := ParseCron(cronExpr)
	if parseErr != nil {
		// A schedule with an unparseable expression would be re-claimed
		// forever (it stays the earliest due row), starving every other
		// schedule. Disable it instead so the cron loop keeps making progress.
		_, disableErr := tx.Exec(ctx, t.setScheduleEnabledSQL(), scheduleID, false, time.Now())
		if disableErr == nil {
			disableErr = tx.Commit(ctx)
		}
		if disableErr != nil {
			_ = tx.Rollback(ctx)
			return false, fmt.Errorf("disable schedule %s with invalid cron %q: %w", scheduleID, cronExpr, disableErr)
		}
		w.logError("flows: disabled schedule with invalid cron expression",
			"schedule_id", scheduleID, "cron_expr", cronExpr, "error", parseErr)
		return true, nil
	}

	// Create the run.
	now := time.Now()
	runIDStr, err := newUUIDv7(now)
	if err != nil {
		return false, fmt.Errorf("generate run id: %w", err)
	}
	runID := RunID(runIDStr)

	shardCount := w.DBConfig.shardCount()
	shard := workflowNameShard(workflowName, runID, shardCount)

	_, err = tx.Exec(ctx, t.insertRunSQL(), shard, string(runID), workflowName, runStatusQueued, inputJSON)
	if err != nil {
		return false, fmt.Errorf("insert cron run: %w", err)
	}

	// Advance the schedule.
	nextRun := sched.Next(now)
	_, err = tx.Exec(ctx, t.advanceScheduleSQL(), scheduleID, nextRun, now)
	if err != nil {
		return false, fmt.Errorf("advance schedule %s: %w", scheduleID, err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		return false, fmt.Errorf("commit cron run: %w", err)
	}

	// Hint workflow workers that a new run is available.
	ch := w.notifyChannel()
	_, _ = w.Pool.Exec(ctx, "SELECT pg_notify($1, $2)", ch, shard+":"+string(runID))

	return true, nil
}

func (w *Worker) nextScheduleDueAt(ctx context.Context) (*time.Time, error) {
	t := newDBTables(w.DBConfig)

	var nextRunAt time.Time
	err := w.Pool.QueryRow(ctx, t.nextScheduleDueAtSQL()).Scan(&nextRunAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "3F000" || pgErr.Code == "42P01" {
				return nil, nil
			}
		}
		return nil, err
	}
	return &nextRunAt, nil
}
