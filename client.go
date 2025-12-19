package flows

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Client is used by API servers to start runs and publish events.
//
// All methods have a `Tx` variant so you can call them inside your own transaction.
type Client struct {
	Codec    Codec
	Now      func() time.Time
	DBConfig DBConfig

	// NotifyChannel overrides the Postgres channel name used for pg_notify.
	// If empty, a safe default is used.
	NotifyChannel string
}

func (c Client) codec() Codec {
	if c.Codec == nil {
		return JSONCodec{}
	}
	return c.Codec
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Client) tables() dbTables {
	return newDBTables(c.DBConfig)
}

func (c Client) notifyChannel() string {
	return normalizeNotifyChannel(c.NotifyChannel)
}

// BeginTx enqueues a workflow run.
//
// Go does not support type parameters on methods, so this is a package-level generic.
func BeginTx[I any, O any](ctx context.Context, c Client, tx pgx.Tx, wf Workflow[I, O], in *I) (RunKey, error) {
	if wf == nil {
		return RunKey{}, fmt.Errorf("workflow is nil")
	}
	inputJSON, err := c.codec().Marshal(in)
	if err != nil {
		return RunKey{}, fmt.Errorf("marshal input: %w", err)
	}

	runIDStr, err := newUUIDv7(c.now())
	if err != nil {
		return RunKey{}, fmt.Errorf("generate run id: %w", err)
	}
	runID := RunID(runIDStr)
	t := c.tables()
	workflowName := wf.Name()
	shard := workflowNameShard(workflowName, runID, c.DBConfig.shardCount())
	runKey := RunKey{WorkflowNameShard: shard, RunID: runID}

	_, err = tx.Exec(ctx, t.insertRunSQL(), shard, string(runID), workflowName, runStatusQueued, inputJSON)
	if err != nil {
		return RunKey{}, fmt.Errorf("insert run: %w", err)
	}

	// Hint workers that a runnable run exists.
	// Notification is best-effort; polling is the fallback.
	_, _ = tx.Exec(ctx, "SELECT pg_notify($1, $2)", c.notifyChannel(), shard+":"+string(runID))
	return runKey, nil
}

// PublishEventTx publishes (run-scoped) event payload, and wakes the run if it is waiting.
//
// Go does not support type parameters on methods, so this is a package-level generic.
func PublishEventTx[T any](ctx context.Context, c Client, tx pgx.Tx, runKey RunKey, eventName string, payload *T) error {
	if eventName == "" {
		return fmt.Errorf("eventName is empty")
	}
	payloadJSON, err := c.codec().Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	t := c.tables()

	_, err = tx.Exec(ctx, t.upsertEventSQL(), runKey.WorkflowNameShard, string(runKey.RunID), eventName, payloadJSON)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	// If a matching wait exists, satisfy it.
	_, _ = tx.Exec(ctx, t.satisfyEventWaitSQL(), runKey.WorkflowNameShard, string(runKey.RunID), waitTypeEvent, eventName, payloadJSON)

	// Wake the run.
	_, _ = tx.Exec(ctx, t.wakeRunFromEventSQL(), runKey.WorkflowNameShard, string(runKey.RunID), runStatusQueued, runStatusWaitingEvent)

	// Hint workers that this run may now be runnable.
	// Notification is best-effort; polling is the fallback.
	_, _ = tx.Exec(ctx, "SELECT pg_notify($1, $2)", c.notifyChannel(), runKey.WorkflowNameShard+":"+string(runKey.RunID))

	return nil
}

// RunStatus represents the current state of a workflow run.
type RunStatus struct {
	Status     string // queued, running, sleeping, waiting_event, completed, failed, cancelled
	Error      string // error message if failed
	CreatedAt  time.Time
	UpdatedAt  time.Time
	NextWakeAt *time.Time // when the run will wake (for sleeping status)
}

// GetRunStatusTx retrieves the current status of a workflow run.
// Returns an error if the run is not found.
func GetRunStatusTx(ctx context.Context, c Client, tx pgx.Tx, runKey RunKey) (*RunStatus, error) {
	t := c.tables()

	var status RunStatus
	var errorText *string
	err := tx.QueryRow(ctx, t.getRunStatusSQL(), runKey.WorkflowNameShard, string(runKey.RunID)).Scan(
		&status.Status,
		&errorText,
		&status.CreatedAt,
		&status.UpdatedAt,
		&status.NextWakeAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("run not found: %s", runKey.RunID)
		}
		return nil, fmt.Errorf("query run status: %w", err)
	}
	if errorText != nil {
		status.Error = *errorText
	}
	return &status, nil
}

// CancelRunTx cancels a workflow run that is queued, sleeping, or waiting for an event.
// Runs that are currently running, already completed, already failed, or already cancelled
// cannot be cancelled and will return an error.
func CancelRunTx(ctx context.Context, c Client, tx pgx.Tx, runKey RunKey) error {
	t := c.tables()

	// Only cancel runs in interruptible states
	result, err := tx.Exec(ctx, t.cancelRunSQL(),
		runKey.WorkflowNameShard,
		string(runKey.RunID),
		runStatusCancelled,
		runStatusQueued,
		runStatusSleeping,
		runStatusWaitingEvent,
	)
	if err != nil {
		return fmt.Errorf("cancel run: %w", err)
	}

	if result.RowsAffected() == 0 {
		// Check if run exists and why it couldn't be cancelled
		var currentStatus string
		err := tx.QueryRow(ctx, t.getRunStatusOnlySQL(), runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&currentStatus)
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("run not found: %s", runKey.RunID)
			}
			return fmt.Errorf("query run status: %w", err)
		}
		return fmt.Errorf("cannot cancel run in status %q", currentStatus)
	}

	return nil
}

// GetRunOutputTx retrieves the output of a completed workflow run.
// Returns an error if the run is not found or not completed.
func GetRunOutputTx[O any](ctx context.Context, c Client, tx pgx.Tx, runKey RunKey) (*O, error) {
	t := c.tables()

	var status string
	var outputJSON []byte
	err := tx.QueryRow(ctx, t.getRunOutputSQL(), runKey.WorkflowNameShard, string(runKey.RunID)).Scan(&status, &outputJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("run not found: %s", runKey.RunID)
		}
		return nil, fmt.Errorf("query run output: %w", err)
	}

	if status != runStatusCompleted {
		return nil, fmt.Errorf("run is not completed (status: %s)", status)
	}

	var out O
	if err := c.codec().Unmarshal(outputJSON, &out); err != nil {
		return nil, fmt.Errorf("unmarshal run output: %w", err)
	}
	return &out, nil
}
