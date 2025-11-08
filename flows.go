package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	if cfg.tx != nil {
		// Use provided transaction
		pgxTx, ok := cfg.tx.(pgx.Tx)
		if !ok {
			return nil, fmt.Errorf("invalid transaction type")
		}

		if err := store.CreateWorkflow(ctx, wfModel, pgxTx); err != nil {
			return nil, fmt.Errorf("failed to create workflow: %w", err)
		}

		if err := store.EnqueueTask(ctx, taskModel, pgxTx); err != nil {
			return nil, fmt.Errorf("failed to enqueue task: %w", err)
		}
	} else {
		// Create internal transaction
		tx, err := store.Pool().Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		if err := store.CreateWorkflow(ctx, wfModel, tx); err != nil {
			return nil, fmt.Errorf("failed to create workflow: %w", err)
		}

		if err := store.EnqueueTask(ctx, taskModel, tx); err != nil {
			return nil, fmt.Errorf("failed to enqueue task: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("failed to commit transaction: %w", err)
		}
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

	// Execute within transaction if provided
	if cfg.tx != nil {
		pgxTx, ok := cfg.tx.(pgx.Tx)
		if !ok {
			return fmt.Errorf("invalid transaction type")
		}
		return store.CreateSignal(ctx, signalModel, pgxTx)
	}

	return store.CreateSignal(ctx, signalModel, nil)
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
	if cfg.tx != nil {
		pgxTx, ok := cfg.tx.(pgx.Tx)
		if !ok {
			return uuid.Nil, fmt.Errorf("invalid transaction type")
		}

		if err := eng.store.CreateWorkflow(ctx, wfModel, pgxTx); err != nil {
			return uuid.Nil, fmt.Errorf("failed to create workflow: %w", err)
		}

		if err := eng.store.EnqueueTask(ctx, taskModel, pgxTx); err != nil {
			return uuid.Nil, fmt.Errorf("failed to enqueue task: %w", err)
		}

		if err := eng.store.UpdateDLQRerun(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(dlqID), storage.UUIDToPgtype(newWorkflowID)); err != nil {
			return uuid.Nil, fmt.Errorf("failed to update DLQ: %w", err)
		}
	} else {
		tx, err := eng.store.Pool().Begin(ctx)
		if err != nil {
			return uuid.Nil, fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		if err := eng.store.CreateWorkflow(ctx, wfModel, tx); err != nil {
			return uuid.Nil, fmt.Errorf("failed to create workflow: %w", err)
		}

		if err := eng.store.EnqueueTask(ctx, taskModel, tx); err != nil {
			return uuid.Nil, fmt.Errorf("failed to enqueue task: %w", err)
		}

		if err := eng.store.UpdateDLQRerun(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(dlqID), storage.UUIDToPgtype(newWorkflowID)); err != nil {
			return uuid.Nil, fmt.Errorf("failed to update DLQ: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return uuid.Nil, fmt.Errorf("failed to commit transaction: %w", err)
		}
	}

	return newWorkflowID, nil
}

// Global engine instance (can be set by user or created automatically)
var globalEngine *Engine

// SetEngine sets the global engine instance.
func SetEngine(engine *Engine) {
	globalEngine = engine
}

// Start starts a workflow using the global engine.
func Start[In, Out any](
	ctx context.Context,
	wf *Workflow[In, Out],
	input *In,
	opts ...StartOption,
) (*Execution[Out], error) {
	if globalEngine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return startInternal(globalEngine.store, ctx, wf, input, opts...)
}

// SendSignal sends a signal using the global engine.
func SendSignal[P any](
	ctx context.Context,
	workflowID uuid.UUID,
	signalName string,
	payload *P,
	opts ...SignalOption,
) error {
	if globalEngine == nil {
		return fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return sendSignalInternal(globalEngine.store, ctx, workflowID, signalName, payload, opts...)
}

// Query queries workflow status using the global engine.
func Query(ctx context.Context, workflowID uuid.UUID) (*WorkflowInfo, error) {
	if globalEngine == nil {
		return nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return globalEngine.Query(ctx, workflowID)
}

// RerunFromDLQ reruns from DLQ using the global engine.
func RerunFromDLQ(ctx context.Context, dlqID uuid.UUID, opts ...RerunOption) (uuid.UUID, error) {
	if globalEngine == nil {
		return uuid.Nil, fmt.Errorf("engine not initialized, call SetEngine first")
	}
	return globalEngine.RerunFromDLQ(ctx, dlqID, opts...)
}
