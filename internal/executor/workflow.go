package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/nvcnvn/flows/internal/registry"
	"github.com/nvcnvn/flows/internal/storage"
)

// WorkflowExecutor executes workflows with deterministic replay.
type WorkflowExecutor struct {
	store *storage.Store
}

// NewWorkflowExecutor creates a new workflow executor.
func NewWorkflowExecutor(store *storage.Store) *WorkflowExecutor {
	return &WorkflowExecutor{store: store}
}

// Execute executes a workflow with replay support using panic/recover.
func (e *WorkflowExecutor) Execute(ctx context.Context, workflowID uuid.UUID, workflow interface{}) error {
	tenantID := flows.MustGetTenantID(ctx)

	// Load workflow state
	wf, err := e.store.GetWorkflow(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
	if err != nil {
		return fmt.Errorf("failed to load workflow: %w", err)
	}

	// Update status to running
	if err := e.store.UpdateWorkflowStatus(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), string(flows.StatusRunning)); err != nil {
		return fmt.Errorf("failed to update workflow status: %w", err)
	}

	// Deserialize input
	input, err := registry.Deserialize(wf.Name, wf.Input)
	if err != nil {
		return fmt.Errorf("failed to deserialize input: %w", err)
	}

	// Load activity results for replay
	activityResults := make(map[int]interface{})
	if len(wf.ActivityResults) > 0 {
		var resultsMap map[string]json.RawMessage
		if err := json.Unmarshal(wf.ActivityResults, &resultsMap); err == nil {
			for seqStr, resultData := range resultsMap {
				var seq int
				fmt.Sscanf(seqStr, "%d", &seq)

				// Deserialize activity result
				var resultInfo struct {
					TypeName string          `json:"type"`
					Data     json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(resultData, &resultInfo); err == nil {
					result, _ := registry.Deserialize(resultInfo.TypeName, resultInfo.Data)
					activityResults[seq] = result
				}
			}
		}
	}

	// Create workflow context with pause function
	wfCtx := e.createWorkflowContext(ctx, workflowID, tenantID, input, wf.SequenceNum, activityResults)

	// Execute workflow function with panic/recover for pause mechanism
	var result interface{}
	var execErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				if err, ok := r.(error); ok && err == flows.ErrWorkflowPaused {
					// Normal pause - workflow needs to wait for activity/timer/signal
					execErr = flows.ErrWorkflowPaused
					return
				}
				// Unexpected panic - re-panic
				panic(r)
			}
		}()

		// Execute workflow function based on type
		result, execErr = e.executeWorkflowFunction(wfCtx, workflow, input)
	}()

	// Handle execution result
	if execErr == flows.ErrWorkflowPaused {
		// Workflow paused - state already persisted
		return nil
	}

	if execErr != nil {
		// Workflow failed
		if err := e.store.UpdateWorkflowFailed(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), execErr.Error()); err != nil {
			return fmt.Errorf("failed to mark workflow as failed: %w", err)
		}

		// Add to DLQ
		if err := e.moveToDLQ(ctx, wf, execErr); err != nil {
			return fmt.Errorf("failed to move to DLQ: %w", err)
		}

		return nil
	}

	// Workflow completed successfully
	outputData, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to serialize output: %w", err)
	}

	if err := e.store.UpdateWorkflowComplete(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), outputData); err != nil {
		return fmt.Errorf("failed to mark workflow as completed: %w", err)
	}

	return nil
}

// createWorkflowContext creates a workflow context with replay state.
func (e *WorkflowExecutor) createWorkflowContext(
	ctx context.Context,
	workflowID uuid.UUID,
	tenantID uuid.UUID,
	input interface{},
	sequenceNum int,
	activityResults map[int]interface{},
) interface{} {
	// This is a simplified version - in reality, we'd need to handle generics properly
	// For now, return a placeholder
	return nil
}

// executeWorkflowFunction executes the workflow function.
func (e *WorkflowExecutor) executeWorkflowFunction(ctx interface{}, workflow interface{}, input interface{}) (interface{}, error) {
	// This would be implemented with reflection or type assertions
	// to call the workflow function with the correct types
	return nil, nil
}

// moveToDLQ moves a failed workflow to the dead letter queue.
func (e *WorkflowExecutor) moveToDLQ(ctx context.Context, wf *storage.WorkflowModel, err error) error {
	dlq := &storage.DLQModel{
		ID:              storage.UUIDToPgtype(uuid.New()),
		TenantID:        wf.TenantID,
		WorkflowID:      wf.ID,
		WorkflowName:    wf.Name,
		WorkflowVersion: wf.Version,
		Input:           wf.Input,
		Error:           err.Error(),
		Attempt:         1,
		Metadata:        nil,
	}

	return e.store.CreateDLQEntry(ctx, dlq)
}

// pauseFunc is called when workflow needs to pause for activity/timer/signal.
func pauseFunc() error {
	panic(flows.ErrWorkflowPaused)
}
