package flows

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
)

type yieldKind string

const (
	yieldSleep yieldKind = "sleep"
	yieldEvent yieldKind = "event"
)

type yieldPanic struct {
	kind yieldKind
}

func (y yieldPanic) Error() string { return "flows: yield(" + string(y.kind) + ")" }

// StepPanicError wraps a panic that occurred during step execution.
type StepPanicError struct {
	Value any
	Stack string
}

func (e StepPanicError) Error() string {
	return fmt.Sprintf("flows: step panicked: %v", e.Value)
}

// WorkflowPanicError wraps a panic that occurred during workflow execution.
//
// This is distinct from StepPanicError: step panics are caught inside Execute,
// while this covers panics in the workflow function itself.
type WorkflowPanicError struct {
	Value any
	Stack string
}

func (e WorkflowPanicError) Error() string {
	if e.Stack == "" {
		return fmt.Sprintf("flows: workflow panicked: %v", e.Value)
	}
	return fmt.Sprintf("flows: workflow panicked: %v\n%s", e.Value, e.Stack)
}

// executeStepWithRecovery executes a step with optional timeout and panic recovery.
// This ensures a step panic doesn't crash the entire worker.
func executeStepWithRecovery[I any, O any](ctx context.Context, step Step[I, O], in *I, timeout time.Duration) (out *O, err error) {
	// Apply timeout if specified
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Recover from panics in step execution
	defer func() {
		if r := recover(); r != nil {
			// Capture stack trace for debugging
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			stack := string(buf[:n])
			err = StepPanicError{Value: r, Stack: stack}
		}
	}()

	return step(ctx, in)
}

// Context is passed to workflow code. It provides replay-safe primitives.
//
// All reads/writes are performed inside the `pgx.Tx` associated with the current worker attempt.
type Context struct {
	runKey RunKey
	tx     pgx.Tx
	codec  Codec
	now    func() time.Time
	t      dbTables
}

func newContext(runKey RunKey, tx pgx.Tx, codec Codec, t dbTables) *Context {
	if codec == nil {
		codec = JSONCodec{}
	}
	return &Context{runKey: runKey, tx: tx, codec: codec, now: time.Now, t: t}
}

func (c *Context) RunID() RunID { return c.runKey.RunID }

func (c *Context) RunKey() RunKey { return c.runKey }

// Tx exposes the underlying transaction, so steps can do ACID business writes.
func (c *Context) Tx() pgx.Tx { return c.tx }

// Execute runs a step exactly-once per (run_id, step_key) by memoizing its successful output.
func Execute[I any, O any](ctx context.Context, c *Context, stepKey string, step Step[I, O], in *I, retry RetryPolicy) (*O, error) {
	if stepKey == "" {
		return nil, errors.New("flows: stepKey must not be empty")
	}

	// Check context cancellation before doing any work.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Fast path: already completed.
	{
		var status string
		var outputJSON []byte
		err := c.tx.QueryRow(ctx,
			c.t.selectStepStatusSQL(),
			c.runKey.WorkflowNameShard, string(c.runKey.RunID), stepKey,
		).Scan(&status, &outputJSON)
		if err == nil && status == stepStatusCompleted {
			var out O
			if err := c.codec.Unmarshal(outputJSON, &out); err != nil {
				return nil, fmt.Errorf("unmarshal step output: %w", err)
			}
			return &out, nil
		}
	}

	maxRetries := retry.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context before each retry attempt.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		out, err := executeStepWithRecovery(ctx, step, in, retry.StepTimeout)
		if err == nil {
			outputJSON, err := c.codec.Marshal(out)
			if err != nil {
				return nil, fmt.Errorf("marshal step output: %w", err)
			}
			inputJSON, err := c.codec.Marshal(in)
			if err != nil {
				return nil, fmt.Errorf("marshal step input: %w", err)
			}

			_, err = c.tx.Exec(ctx, c.t.upsertStepCompletedSQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), stepKey, stepStatusCompleted, inputJSON, outputJSON, attempt+1)
			if err != nil {
				return nil, fmt.Errorf("persist step output: %w", err)
			}
			return out, nil
		}

		lastErr = err
		_, _ = c.tx.Exec(ctx, c.t.upsertStepFailedSQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), stepKey, stepStatusFailed, err.Error(), attempt+1)

		if attempt < maxRetries {
			if retry.Backoff != nil {
				d := time.Duration(retry.Backoff(attempt)) * time.Millisecond
				if d > 0 {
					t := time.NewTimer(d)
					select {
					case <-ctx.Done():
						t.Stop()
						return nil, ctx.Err()
					case <-t.C:
					}
				}
			}
			continue
		}
	}

	return nil, lastErr
}

// Sleep durably yields execution until now()+duration.
//
// If the sleep already elapsed (on resume), it returns immediately.
func Sleep(ctx context.Context, c *Context, waitKey string, duration time.Duration) {
	if waitKey == "" {
		panic("flows: waitKey must not be empty")
	}

	wakeAt := c.now().Add(duration)

	// If we already have a sleep wait and it has elapsed, return.
	{
		var wakeAtDB time.Time
		var satisfiedAt *time.Time
		err := c.tx.QueryRow(ctx,
			c.t.selectWaitStateSQL(),
			c.runKey.WorkflowNameShard, string(c.runKey.RunID), waitKey, waitTypeSleep,
		).Scan(&wakeAtDB, &satisfiedAt)
		if err == nil {
			if satisfiedAt != nil {
				return
			}
			if !c.now().Before(wakeAtDB) {
				_, _ = c.tx.Exec(ctx,
					c.t.satisfySleepWaitSQL(),
					c.runKey.WorkflowNameShard, string(c.runKey.RunID), waitKey,
				)
				return
			}
			wakeAt = wakeAtDB
		}
	}

	_, err := c.tx.Exec(ctx, c.t.upsertSleepWaitSQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), waitKey, waitTypeSleep, wakeAt)
	if err != nil {
		panic(fmt.Errorf("persist sleep wait: %w", err))
	}

	_, err = c.tx.Exec(ctx,
		c.t.setRunSleepingSQL(),
		c.runKey.WorkflowNameShard, string(c.runKey.RunID), runStatusSleeping, wakeAt,
	)
	if err != nil {
		panic(fmt.Errorf("persist run sleep state: %w", err))
	}

	panic(yieldPanic{kind: yieldSleep})
}

// WaitForEvent blocks until the event is published for this run.
//
// The event is memoized by (run_id, wait_key). On replay, it returns the same payload.
func WaitForEvent[T any](ctx context.Context, c *Context, waitKey string, eventName string) *T {
	if waitKey == "" {
		panic("flows: waitKey must not be empty")
	}
	if eventName == "" {
		panic("flows: eventName must not be empty")
	}

	// If satisfied, return payload.
	{
		var payloadJSON []byte
		var satisfiedAt *time.Time
		err := c.tx.QueryRow(ctx,
			c.t.selectWaitPayloadSQL(),
			c.runKey.WorkflowNameShard, string(c.runKey.RunID), waitKey, waitTypeEvent,
		).Scan(&payloadJSON, &satisfiedAt)
		if err == nil && satisfiedAt != nil {
			var out T
			if err := c.codec.Unmarshal(payloadJSON, &out); err != nil {
				panic(fmt.Errorf("unmarshal event payload: %w", err))
			}
			return &out
		}
	}

	// If an event row already exists, consume and mark satisfied.
	{
		var payloadJSON []byte
		err := c.tx.QueryRow(ctx,
			c.t.selectEventPayloadSQL(),
			c.runKey.WorkflowNameShard, string(c.runKey.RunID), eventName,
		).Scan(&payloadJSON)
		if err == nil {
			_, _ = c.tx.Exec(ctx, c.t.upsertSatisfiedEventWaitSQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), waitKey, waitTypeEvent, eventName, payloadJSON)

			var out T
			if err := c.codec.Unmarshal(payloadJSON, &out); err != nil {
				panic(fmt.Errorf("unmarshal event payload: %w", err))
			}
			return &out
		}
	}

	// Otherwise, persist the wait and yield.
	_, err := c.tx.Exec(ctx, c.t.upsertEventWaitSQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), waitKey, waitTypeEvent, eventName)
	if err != nil {
		panic(fmt.Errorf("persist event wait: %w", err))
	}

	_, err = c.tx.Exec(ctx,
		c.t.setRunWaitingEventSQL(),
		c.runKey.WorkflowNameShard, string(c.runKey.RunID), runStatusWaitingEvent,
	)
	if err != nil {
		panic(fmt.Errorf("persist run event state: %w", err))
	}

	panic(yieldPanic{kind: yieldEvent})
}

// RandomUUIDv7 returns a deterministic UUIDv7 for this run and key.
func RandomUUIDv7(ctx context.Context, c *Context, key string) string {
	if key == "" {
		panic("flows: key must not be empty")
	}

	var val string
	err := c.tx.QueryRow(ctx,
		c.t.selectRandomUUIDv7SQL(),
		c.runKey.WorkflowNameShard, string(c.runKey.RunID), key,
	).Scan(&val)
	if err == nil {
		return val
	}

	uuid, err := newUUIDv7(c.now())
	if err != nil {
		panic(fmt.Errorf("generate uuidv7: %w", err))
	}

	_, err = c.tx.Exec(ctx, c.t.upsertRandomUUIDv7SQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), key, uuid)
	if err != nil {
		panic(fmt.Errorf("persist uuidv7: %w", err))
	}
	return uuid
}

// RandomUint64 returns a deterministic random uint64 for this run and key.
func RandomUint64(ctx context.Context, c *Context, key string) uint64 {
	if key == "" {
		panic("flows: key must not be empty")
	}

	var val int64
	err := c.tx.QueryRow(ctx,
		c.t.selectRandomUint64SQL(),
		c.runKey.WorkflowNameShard, string(c.runKey.RunID), key,
	).Scan(&val)
	if err == nil {
		return uint64(val)
	}

	u, err := newCryptoUint64()
	if err != nil {
		panic(fmt.Errorf("generate uint64: %w", err))
	}

	_, err = c.tx.Exec(ctx, c.t.upsertRandomUint64SQL(), c.runKey.WorkflowNameShard, string(c.runKey.RunID), key, int64(u))
	if err != nil {
		panic(fmt.Errorf("persist uint64: %w", err))
	}

	return u
}
