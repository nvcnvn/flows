package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows/internal/registry"
	"github.com/nvcnvn/flows/internal/storage"
)

// Engine manages workflow execution and storage.
type Engine struct {
	store   *storage.Store
	sharder Sharder
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithSharder sets a custom sharder for the engine.
// If not provided, the engine will use the global sharder.
//
// Example with ShardConfig:
//
//	sharder := flows.NewShardConfig(16)
//	engine := flows.NewEngine(pool, flows.WithSharder(sharder))
//
// Example with custom sharder:
//
//	type MyCustomSharder struct {}
//	func (s *MyCustomSharder) GetShard(workflowID uuid.UUID) int { /* ... */ }
//	func (s *MyCustomSharder) NumShards() int { return 10 }
//
//	engine := flows.NewEngine(pool, flows.WithSharder(&MyCustomSharder{}))
func WithSharder(sharder Sharder) EngineOption {
	return func(e *Engine) {
		e.sharder = sharder
	}
}

// WithShardConfig is a convenience function for WithSharder that accepts a ShardConfig.
// For custom sharder implementations, use WithSharder instead.
func WithShardConfig(config *ShardConfig) EngineOption {
	return WithSharder(config)
}

// NewEngine creates a new workflow engine with PostgreSQL storage.
// This is the main entry point for initializing the flows library.
//
// Example:
//
//	pool, err := pgxpool.New(ctx, "postgres://localhost/mydb")
//	if err != nil {
//		log.Fatal(err)
//	}
//	engine := flows.NewEngine(pool)
//	flows.SetEngine(engine)
//
// Example with custom shard configuration:
//
//	sharder := flows.NewShardConfig(16) // 16 shards
//	engine := flows.NewEngine(pool, flows.WithSharder(sharder))
func NewEngine(pool *pgxpool.Pool, opts ...EngineOption) *Engine {
	eng := &Engine{
		store:   storage.NewStore(pool),
		sharder: nil, // Will use global sharder if not set
	}
	for _, opt := range opts {
		opt(eng)
	}
	return eng
}

// Store returns the underlying storage.
func (eng *Engine) Store() *storage.Store {
	return eng.store
}

// executeInTx executes a function within a transaction.
// If tx is provided, it uses that transaction; otherwise, it creates a new one.
// The function fn receives the transaction to use for its operations.
func executeInTx(ctx context.Context, store *storage.Store, tx Tx, fn func(Tx) error) error {
	if tx != nil {
		// Use provided transaction but do NOT attempt to manage its lifecycle here.
		// The caller is responsible for committing or rolling back.
		return fn(tx)
	}

	// Create new transaction
	newTx, err := store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := newTx.Rollback(ctx); rollbackErr != nil && rollbackErr != pgx.ErrTxClosed {
			slog.Error("executeInTx Rollback error", "error", rollbackErr)
		}
	}()

	if err := fn(newTx); err != nil {
		return err
	}

	if err := newTx.Commit(ctx); err != nil {
		return err
	}

	committed = true
	return nil
}

// startInternal is the internal implementation of Start.
func startInternal[In, Out any](
	store *storage.Store,
	sharder Sharder,
	ctx context.Context,
	wf *Workflow[In, Out],
	input *In,
	opts ...StartOption,
) (*Execution[Out], error) {
	cfg := getStartConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Serialize input
	_, inputData, err := registry.Serialize(input)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize input: %w", err)
	}

	workflowID := uuid.New()

	// Get sharded workflow name using simple hash-based distribution
	baseName := wf.Name()
	shardedName := getShardedWorkflowName(baseName, workflowID, sharder)

	// Create workflow model
	wfModel := &storage.WorkflowModel{
		ID:              storage.UUIDToPgtype(workflowID),
		TenantID:        storage.UUIDToPgtype(tenantID),
		Name:            shardedName, // Use sharded name in database
		Version:         wf.Version(),
		Status:          string(StatusPending),
		Input:           inputData,
		SequenceNum:     0,
		ActivityResults: []byte("{}"),
	}

	// Create task for workflow execution
	taskID := uuid.New()
	taskModel := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(taskID),
		TenantID:          storage.UUIDToPgtype(tenantID),
		WorkflowID:        storage.UUIDToPgtype(workflowID),
		WorkflowName:      shardedName, // Use sharded name in database
		TaskType:          string(TaskTypeWorkflow),
		VisibilityTimeout: time.Now(),
	}

	// Execute within transaction
	err = executeInTx(ctx, store, cfg.tx, func(tx Tx) error {
		if err := store.CreateWorkflow(ctx, wfModel, tx); err != nil {
			return fmt.Errorf("failed to create workflow: %w", err)
		}
		if err := store.EnqueueTask(ctx, taskModel, tx); err != nil {
			return fmt.Errorf("failed to enqueue task: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &Execution[Out]{
		id:                  taskID,
		workflowID:          workflowID,
		workflowName:        baseName,    // Return base name to user
		workflowNameSharded: shardedName, // Also provide sharded name for debugging
		store:               store,
	}, nil
}

// sendSignalInternal is the internal implementation of SendSignal.
func sendSignalInternal[P any](
	store *storage.Store,
	sharder Sharder,
	ctx context.Context,
	workflowID uuid.UUID,
	workflowName string, // Base workflow name (user-defined)
	signalName string,
	payload *P,
	opts ...SignalOption,
) error {
	cfg := getSignalConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Derive sharded name from base name and workflow ID
	shardedName := getShardedWorkflowName(workflowName, workflowID, sharder)

	// Serialize payload
	_, payloadData, err := registry.Serialize(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize payload: %w", err)
	}

	signalModel := &storage.SignalModel{
		WorkflowName: shardedName, // Use sharded name in database
		ID:           storage.UUIDToPgtype(uuid.New()),
		TenantID:     storage.UUIDToPgtype(tenantID),
		WorkflowID:   storage.UUIDToPgtype(workflowID),
		SignalName:   signalName,
		Payload:      payloadData,
		Consumed:     false,
	}

	// Create workflow task to resume execution
	taskModel := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(uuid.New()),
		TenantID:          storage.UUIDToPgtype(tenantID),
		WorkflowID:        storage.UUIDToPgtype(workflowID),
		WorkflowName:      shardedName, // Use sharded name in database
		TaskType:          string(TaskTypeWorkflow),
		VisibilityTimeout: time.Now(),
	}

	// Execute within transaction
	return executeInTx(ctx, store, cfg.tx, func(tx Tx) error {
		// Verify workflow exists and belongs to the correct tenant
		// This ensures cross-tenant signal attempts are rejected
		_, err := store.GetWorkflow(ctx, shardedName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
		if err != nil {
			return fmt.Errorf("failed to get workflow: %w", err)
		}

		if err := store.CreateSignal(ctx, signalModel, tx); err != nil {
			return fmt.Errorf("failed to create signal: %w", err)
		}
		if err := store.EnqueueTask(ctx, taskModel, tx); err != nil {
			return fmt.Errorf("failed to enqueue task: %w", err)
		}
		return nil
	})
}

// Query retrieves the current status of a workflow.
// workflowName should be the base workflow name (user-defined constant).
func (eng *Engine) Query(ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*WorkflowInfo, error) {
	cfg := getQueryConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Derive sharded name from base name and workflow ID
	shardedName := getShardedWorkflowName(workflowName, workflowID, eng.sharder)

	// Use efficient single-shard query with sharded workflow_name
	wf, err := eng.store.GetWorkflow(ctx, shardedName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), cfg.tx)
	if err != nil {
		return nil, err
	}

	return &WorkflowInfo{
		ID:       storage.PgtypeToUUID(wf.ID).String(),
		TenantID: storage.PgtypeToUUID(wf.TenantID).String(),
		Name:     wf.Name,
		Version:  wf.Version,
		Status:   WorkflowStatus(wf.Status),
		Error:    wf.Error,
		Output:   wf.Output,
	}, nil
}

// RerunFromDLQ creates a new workflow execution from a DLQ entry.
// workflowName should be the base workflow name (user-defined constant).
func (eng *Engine) RerunFromDLQ(
	ctx context.Context,
	workflowName string,
	dlqID uuid.UUID,
	opts ...RerunOption,
) (uuid.UUID, error) {
	cfg := getRerunConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Note: DLQ entries store the sharded workflow name, so we need to search across all shards
	// for this base workflow name. This is less efficient but DLQ operations are rare.
	// In practice, you'd likely list DLQ entries first which would give you the exact sharded name.

	// For now, we'll try to get the DLQ entry directly if the user provided the sharded name,
	// or derive it if they have the original workflow ID (which is stored in DLQ).
	// This is a simplified implementation - production systems might want to add a base_name column to DLQ.

	dlqEntry, err := eng.store.GetDLQEntry(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(dlqID))
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get DLQ entry: %w", err)
	}
	if dlqEntry == nil {
		return uuid.Nil, fmt.Errorf("DLQ entry not found")
	}

	// Create new workflow with same input
	newWorkflowID := uuid.New()

	// Extract base name from the DLQ entry's sharded name
	baseName := extractBaseWorkflowName(dlqEntry.WorkflowName)

	// Derive new sharded name for the new workflow ID
	newShardedName := getShardedWorkflowName(baseName, newWorkflowID, eng.sharder)

	// Create metadata linking to DLQ
	metadata := map[string]interface{}{
		"dlq_id":               dlqID.String(),
		"original_workflow_id": storage.PgtypeToUUID(dlqEntry.WorkflowID).String(),
		"attempt":              dlqEntry.Attempt + 1,
	}
	metadataJSON, _ := json.Marshal(metadata)

	wfModel := &storage.WorkflowModel{
		ID:              storage.UUIDToPgtype(newWorkflowID),
		TenantID:        storage.UUIDToPgtype(tenantID),
		Name:            newShardedName, // Use new sharded name for new workflow
		Version:         dlqEntry.WorkflowVersion,
		Status:          string(StatusPending),
		Input:           dlqEntry.Input,
		SequenceNum:     0,
		ActivityResults: []byte("{}"),
	}

	taskModel := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(uuid.New()),
		TenantID:          storage.UUIDToPgtype(tenantID),
		WorkflowID:        storage.UUIDToPgtype(newWorkflowID),
		WorkflowName:      newShardedName, // Use new sharded name
		TaskType:          string(TaskTypeWorkflow),
		TaskData:          metadataJSON,
		VisibilityTimeout: time.Now(),
	}

	// Execute within transaction
	err = executeInTx(ctx, eng.store, cfg.tx, func(tx Tx) error {
		if err := eng.store.CreateWorkflow(ctx, wfModel, tx); err != nil {
			return fmt.Errorf("failed to create workflow: %w", err)
		}
		if err := eng.store.EnqueueTask(ctx, taskModel, tx); err != nil {
			return fmt.Errorf("failed to enqueue task: %w", err)
		}
		if err := eng.store.UpdateDLQRerun(ctx, dlqEntry.WorkflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(dlqID), storage.UUIDToPgtype(newWorkflowID)); err != nil {
			return fmt.Errorf("failed to update DLQ: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}

	return newWorkflowID, nil
}

// startWith starts a workflow execution using this engine instance.
// This is a wrapper that allows using the engine directly without setting a global engine.
//
// Example:
//
//	engine := flows.NewEngine(pool)
//	exec, err := flows.StartWith(engine, ctx, myWorkflow, input)
func startWith[In, Out any](
	eng *Engine,
	ctx context.Context,
	wf *Workflow[In, Out],
	input *In,
	opts ...StartOption,
) (*Execution[Out], error) {
	return startInternal(eng.store, eng.sharder, ctx, wf, input, opts...)
}

// SendSignalWith sends a signal to a running workflow using a specific engine instance.
// workflowName should be the base workflow name (user-defined constant).
//
// Example:
//
//	err := flows.SendSignalWith(engine, ctx, workflowName, workflowID, "signal-name", payload)
func SendSignalWith[P any](
	eng *Engine,
	ctx context.Context,
	workflowName string,
	workflowID uuid.UUID,
	signalName string,
	payload *P,
	opts ...SignalOption,
) error {
	return sendSignalInternal(eng.store, eng.sharder, ctx, workflowID, workflowName, signalName, payload, opts...)
}

// GetResultWith retrieves the typed result of a completed workflow using a specific engine instance.
// This allows retrieving results without keeping the original Execution handle.
// Returns an error if the workflow is not completed or failed.
// workflowName should be the base workflow name (user-defined constant).
//
// Example:
//
//	result, err := flows.GetResultWith[MyOutput](engine, ctx, workflowName, workflowID)
func GetResultWith[Out any](eng *Engine, ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*Out, error) {
	cfg := getQueryConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Derive sharded name from base name and workflow ID
	shardedName := getShardedWorkflowName(workflowName, workflowID, eng.sharder)

	// Get workflow
	wf, err := eng.store.GetWorkflow(ctx, shardedName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), cfg.tx)
	if err != nil {
		return nil, err
	}

	// Check status
	switch WorkflowStatus(wf.Status) {
	case StatusCompleted:
		// Deserialize output
		if len(wf.Output) == 0 {
			return nil, nil
		}
		var result Out
		if err := json.Unmarshal(wf.Output, &result); err != nil {
			return nil, fmt.Errorf("failed to deserialize output: %w", err)
		}
		return &result, nil

	case StatusFailed:
		if wf.Error != "" {
			return nil, fmt.Errorf("workflow failed: %s", wf.Error)
		}
		return nil, fmt.Errorf("workflow failed with unknown error")

	case StatusRunning:
		return nil, fmt.Errorf("workflow is still running")

	case StatusPending:
		return nil, fmt.Errorf("workflow is pending execution")

	default:
		return nil, fmt.Errorf("unknown workflow status: %s", wf.Status)
	}
}

// WaitForResultWith waits for a workflow to complete and returns the typed result using a specific engine instance.
// This is similar to Execution.Get() but works without keeping the Execution handle.
// Blocks until the workflow finishes or context is cancelled.
// workflowName should be the base workflow name (user-defined constant).
//
// Example:
//
//	result, err := flows.WaitForResultWith[MyOutput](engine, ctx, workflowName, workflowID)
func WaitForResultWith[Out any](eng *Engine, ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*Out, error) {
	cfg := getQueryConfig(opts)
	// Poll for workflow completion
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	tenantID := MustGetTenantID(ctx)

	// Derive sharded name from base name and workflow ID
	shardedName := getShardedWorkflowName(workflowName, workflowID, eng.sharder)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			// Check workflow status
			wf, err := eng.store.GetWorkflow(ctx, shardedName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), cfg.tx)
			if err != nil {
				return nil, err
			}

			switch WorkflowStatus(wf.Status) {
			case StatusCompleted:
				// Deserialize output
				if len(wf.Output) == 0 {
					return nil, nil
				}
				var result Out
				if err := json.Unmarshal(wf.Output, &result); err != nil {
					return nil, fmt.Errorf("failed to deserialize output: %w", err)
				}
				return &result, nil

			case StatusFailed:
				if wf.Error != "" {
					return nil, fmt.Errorf("workflow failed: %s", wf.Error)
				}
				return nil, fmt.Errorf("workflow failed with unknown error")

			case StatusRunning, StatusPending:
				// Continue polling
				continue

			default:
				return nil, fmt.Errorf("unknown workflow status: %s", wf.Status)
			}
		}
	}
}

// QueryWith retrieves the current status of a workflow using a specific engine instance.
// workflowName should be the base workflow name (user-defined constant).
//
// Example:
//
//	info, err := flows.QueryWith(engine, ctx, workflowName, workflowID)
func QueryWith(eng *Engine, ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*WorkflowInfo, error) {
	return eng.Query(ctx, workflowName, workflowID, opts...)
}

// RerunFromDLQWith reruns a workflow from DLQ using a specific engine instance.
// workflowName should be the base workflow name (user-defined constant).
//
// Example:
//
//	newWorkflowID, err := flows.RerunFromDLQWith(engine, ctx, workflowName, dlqID)
func RerunFromDLQWith(eng *Engine, ctx context.Context, workflowName string, dlqID uuid.UUID, opts ...RerunOption) (uuid.UUID, error) {
	return eng.RerunFromDLQ(ctx, workflowName, dlqID, opts...)
}

// Global engine instance (can be set by user or created automatically)
var (
	globalEngine   *Engine
	globalEngineMu sync.RWMutex
)

// SetEngine sets the global engine instance for use with package-level functions.
// This is optional - you can also use Engine methods directly.
//
// Example with global engine:
//
//	engine := flows.NewEngine(pool)
//	flows.SetEngine(engine)
//	exec, err := flows.Start(ctx, myWorkflow, input)
//
// Example without global engine:
//
//	engine := flows.NewEngine(pool)
//	exec, err := engine.Start(ctx, myWorkflow, input)
func SetEngine(engine *Engine) {
	globalEngineMu.Lock()
	defer globalEngineMu.Unlock()
	globalEngine = engine
}

// getGlobalEngine safely retrieves the global engine instance.
func getGlobalEngine() *Engine {
	globalEngineMu.RLock()
	defer globalEngineMu.RUnlock()
	return globalEngine
}

// Start starts a workflow using the global engine.
// Requires SetEngine to be called first.
//
// Example with global engine:
//
//	flows.SetEngine(engine)
//	exec, err := flows.Start(ctx, myWorkflow, input)
//
// Or use StartWith to avoid global state:
//
//	exec, err := flows.StartWith(engine, ctx, myWorkflow, input)
func Start[In, Out any](
	ctx context.Context,
	wf *Workflow[In, Out],
	input *In,
	opts ...StartOption,
) (*Execution[Out], error) {
	engine := getGlobalEngine()
	if engine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return startWith(engine, ctx, wf, input, opts...)
}

// StartWith starts a workflow using a specific engine instance.
// This allows using the engine directly without setting a global engine.
//
// Example:
//
//	engine := flows.NewEngine(pool)
//	exec, err := flows.StartWith(engine, ctx, myWorkflow, input)
func StartWith[In, Out any](
	eng *Engine,
	ctx context.Context,
	wf *Workflow[In, Out],
	input *In,
	opts ...StartOption,
) (*Execution[Out], error) {
	return startWith(eng, ctx, wf, input, opts...)
}

// SendSignal sends a signal using the global engine.
// workflowName should be the base workflow name (user-defined constant).
// Get workflow name from the Execution returned by Start().
//
// Example with global engine:
//
//	flows.SetEngine(engine)
//	exec, _ := flows.Start(ctx, myWorkflow, input)
//	err := flows.SendSignal(ctx, exec.WorkflowName(), exec.WorkflowID(), "signal-name", payload)
//
// Or use SendSignalWith to avoid global state:
//
//	err := flows.SendSignalWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID(), "signal-name", payload)
func SendSignal[P any](
	ctx context.Context,
	workflowName string,
	workflowID uuid.UUID,
	signalName string,
	payload *P,
	opts ...SignalOption,
) error {
	engine := getGlobalEngine()
	if engine == nil {
		return fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return SendSignalWith(engine, ctx, workflowName, workflowID, signalName, payload, opts...)
}

// Query queries workflow status using the global engine.
// workflowName should be the base workflow name (user-defined constant).
// Get workflow name from the Execution returned by Start().
//
// Example with global engine:
//
//	flows.SetEngine(engine)
//	exec, _ := flows.Start(ctx, myWorkflow, input)
//	info, _ := flows.Query(ctx, exec.WorkflowName(), exec.WorkflowID())
//
// Or use QueryWith to avoid global state:
//
//	info, _ := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
func Query(ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*WorkflowInfo, error) {
	engine := getGlobalEngine()
	if engine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return QueryWith(engine, ctx, workflowName, workflowID, opts...)
}

// GetResult retrieves the typed result of a completed workflow using the global engine.
// This allows retrieving results without keeping the original Execution handle.
// Returns an error if the workflow is not completed or failed.
// workflowName should be the base workflow name (user-defined constant).
//
// Example with global engine:
//
//	flows.SetEngine(engine)
//	result, err := flows.GetResult[MyOutput](ctx, workflowName, workflowID)
//
// Or use GetResultWith to avoid global state:
//
//	result, err := flows.GetResultWith[MyOutput](engine, ctx, workflowName, workflowID)
func GetResult[Out any](ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*Out, error) {
	engine := getGlobalEngine()
	if engine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return GetResultWith[Out](engine, ctx, workflowName, workflowID, opts...)
}

// WaitForResult waits for a workflow to complete and returns the typed result.
// This is similar to Execution.Get() but works without keeping the Execution handle.
// Blocks until the workflow finishes or context is cancelled.
// workflowName should be the base workflow name (user-defined constant).
//
// Example with global engine:
//
//	flows.SetEngine(engine)
//	result, err := flows.WaitForResult[MyOutput](ctx, workflowName, workflowID)
//
// Or use WaitForResultWith to avoid global state:
//
//	result, err := flows.WaitForResultWith[MyOutput](engine, ctx, workflowName, workflowID)
func WaitForResult[Out any](ctx context.Context, workflowName string, workflowID uuid.UUID, opts ...QueryOption) (*Out, error) {
	engine := getGlobalEngine()
	if engine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return WaitForResultWith[Out](engine, ctx, workflowName, workflowID, opts...)
}

// RerunFromDLQ reruns from DLQ using the global engine.
// workflowName should be the base workflow name (user-defined constant).
// Get workflow name from listing DLQ entries (e.g., via a ListDLQ function).
//
// Example with global engine:
//
//	flows.SetEngine(engine)
//	newWorkflowID, err := flows.RerunFromDLQ(ctx, workflowName, dlqID)
//
// Or use RerunFromDLQWith to avoid global state:
//
//	newWorkflowID, err := flows.RerunFromDLQWith(engine, ctx, workflowName, dlqID)
func RerunFromDLQ(ctx context.Context, workflowName string, dlqID uuid.UUID, opts ...RerunOption) (uuid.UUID, error) {
	engine := getGlobalEngine()
	if engine == nil {
		return uuid.Nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return RerunFromDLQWith(engine, ctx, workflowName, dlqID, opts...)
}
