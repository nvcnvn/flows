package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows/internal/registry"
	"github.com/nvcnvn/flows/internal/storage"
)

// Engine manages workflow execution and storage.
type Engine struct {
	store *storage.Store
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
func NewEngine(pool *pgxpool.Pool) *Engine {
	return &Engine{store: storage.NewStore(pool)}
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
		// Use provided transaction
		return fn(tx)
	}

	// Create new transaction
	newTx, err := store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		rollbackErr := newTx.Rollback(ctx)
		if err == nil {
			slog.Error("executeInTx Rollback error", "error", rollbackErr)
		}
	}()

	if err := fn(newTx); err != nil {
		return err
	}

	return newTx.Commit(ctx)
}

// startInternal is the internal implementation of Start.
func startInternal[In, Out any](
	store *storage.Store,
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

	// Create workflow model
	wfModel := &storage.WorkflowModel{
		ID:              storage.UUIDToPgtype(workflowID),
		TenantID:        storage.UUIDToPgtype(tenantID),
		Name:            wf.Name(),
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
		WorkflowName:      wf.Name(),
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
		id:         taskID,
		workflowID: workflowID,
		store:      store,
	}, nil
}

// sendSignalInternal is the internal implementation of SendSignal.
func sendSignalInternal[P any](
	store *storage.Store,
	ctx context.Context,
	workflowID uuid.UUID,
	signalName string,
	payload *P,
	opts ...SignalOption,
) error {
	cfg := getSignalConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Serialize payload
	_, payloadData, err := registry.Serialize(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize payload: %w", err)
	}

	signalModel := &storage.SignalModel{
		ID:         storage.UUIDToPgtype(uuid.New()),
		TenantID:   storage.UUIDToPgtype(tenantID),
		WorkflowID: storage.UUIDToPgtype(workflowID),
		SignalName: signalName,
		Payload:    payloadData,
		Consumed:   false,
	}

	// Get workflow info to enqueue task
	wf, err := store.GetWorkflow(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
	if err != nil {
		return fmt.Errorf("failed to get workflow: %w", err)
	}

	// Create workflow task to resume execution
	taskModel := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(uuid.New()),
		TenantID:          storage.UUIDToPgtype(tenantID),
		WorkflowID:        storage.UUIDToPgtype(workflowID),
		WorkflowName:      wf.Name,
		TaskType:          string(TaskTypeWorkflow),
		VisibilityTimeout: time.Now(),
	}

	// Execute within transaction
	return executeInTx(ctx, store, cfg.tx, func(tx Tx) error {
		if err := store.CreateSignal(ctx, signalModel, tx); err != nil {
			return err
		}
		return store.EnqueueTask(ctx, taskModel, tx)
	})
}

// Query retrieves the current status of a workflow.
func (eng *Engine) Query(ctx context.Context, workflowID uuid.UUID) (*WorkflowInfo, error) {
	tenantID := MustGetTenantID(ctx)

	wf, err := eng.store.GetWorkflow(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
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
	}, nil
}

// RerunFromDLQ creates a new workflow execution from a DLQ entry.
func (eng *Engine) RerunFromDLQ(
	ctx context.Context,
	dlqID uuid.UUID,
	opts ...RerunOption,
) (uuid.UUID, error) {
	cfg := getRerunConfig(opts)
	tenantID := MustGetTenantID(ctx)

	// Get DLQ entry
	dlqEntry, err := eng.store.GetDLQEntry(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(dlqID))
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get DLQ entry: %w", err)
	}
	if dlqEntry == nil {
		return uuid.Nil, fmt.Errorf("DLQ entry not found")
	}

	// Create new workflow with same input
	newWorkflowID := uuid.New()

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
		Name:            dlqEntry.WorkflowName,
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
		WorkflowName:      dlqEntry.WorkflowName,
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
		if err := eng.store.UpdateDLQRerun(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(dlqID), storage.UUIDToPgtype(newWorkflowID)); err != nil {
			return fmt.Errorf("failed to update DLQ: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}

	return newWorkflowID, nil
}

// Global engine instance (can be set by user or created automatically)
var (
	globalEngine   *Engine
	globalEngineMu sync.RWMutex
)

// SetEngine sets the global engine instance.
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
	return startInternal(engine.store, ctx, wf, input, opts...)
}

// SendSignal sends a signal using the global engine.
func SendSignal[P any](
	ctx context.Context,
	workflowID uuid.UUID,
	signalName string,
	payload *P,
	opts ...SignalOption,
) error {
	engine := getGlobalEngine()
	if engine == nil {
		return fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return sendSignalInternal(engine.store, ctx, workflowID, signalName, payload, opts...)
}

// Query queries workflow status using the global engine.
func Query(ctx context.Context, workflowID uuid.UUID) (*WorkflowInfo, error) {
	engine := getGlobalEngine()
	if engine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return engine.Query(ctx, workflowID)
}

// RerunFromDLQ reruns from DLQ using the global engine.
func RerunFromDLQ(ctx context.Context, dlqID uuid.UUID, opts ...RerunOption) (uuid.UUID, error) {
	engine := getGlobalEngine()
	if engine == nil {
		return uuid.Nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return engine.RerunFromDLQ(ctx, dlqID, opts...)
}
