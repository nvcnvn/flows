package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows/internal/registry"
	"github.com/nvcnvn/flows/internal/storage"
)

// Worker polls and executes workflow tasks.
type Worker struct {
	store  *storage.Store
	config WorkerConfig
	stopCh chan struct{}
	doneCh chan struct{}
	wg     sync.WaitGroup
}

// WorkerConfig configures worker behavior.
type WorkerConfig struct {
	Concurrency       int           // Number of concurrent task handlers
	WorkflowNames     []string      // Workflow types to handle
	PollInterval      time.Duration // Poll frequency
	VisibilityTimeout time.Duration // Task lock duration
	TenantID          uuid.UUID     // Tenant to work for
}

// NewWorker creates a new worker instance.
// Automatically expands base workflow names to include all shards.
// Example: ["loan-workflow"] -> ["loan-workflow-shard-0", "loan-workflow-shard-1", "loan-workflow-shard-2"]
func NewWorker(pool *pgxpool.Pool, config WorkerConfig) *Worker {
	// Set defaults
	if config.Concurrency == 0 {
		config.Concurrency = 10
	}
	if config.PollInterval == 0 {
		config.PollInterval = 1 * time.Second
	}
	if config.VisibilityTimeout == 0 {
		config.VisibilityTimeout = 5 * time.Minute
	}

	// Expand workflow names to include all shards
	var expandedNames []string
	for _, baseName := range config.WorkflowNames {
		shardedNames := GetAllShardedWorkflowNames(baseName)
		expandedNames = append(expandedNames, shardedNames...)
	}
	config.WorkflowNames = expandedNames

	return &Worker{
		store:  storage.NewStore(pool),
		config: config,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Run starts the worker. Blocks until Stop is called or context is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	defer close(w.doneCh)

	// Create a context that combines both stop signal and parent context
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start worker goroutines
	for i := 0; i < w.config.Concurrency; i++ {
		w.wg.Add(1)
		go w.workerLoop(workerCtx)
	}

	// Wait for stop signal or context cancellation
	select {
	case <-w.stopCh:
		cancel() // Cancel the worker context
	case <-ctx.Done():
		cancel()
	}

	// Wait for all worker goroutines to finish
	w.wg.Wait()
	return nil
}

// Stop gracefully stops the worker.
func (w *Worker) Stop() {
	close(w.stopCh)
	<-w.doneCh
}

// workerLoop polls for tasks and processes them.
func (w *Worker) workerLoop(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.pollAndProcess(ctx); err != nil {
				// Only log if it's not a context cancellation error
				if err != context.Canceled && ctx.Err() == nil {
					fmt.Printf("worker error: %v\n", err)
				}
			}
		}
	}
}

// pollAndProcess polls for a task and processes it.
func (w *Worker) pollAndProcess(ctx context.Context) error {
	// Check if context is already cancelled before doing work
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Add tenant ID to context
	ctx = WithTenantID(ctx, w.config.TenantID)

	// Dequeue task
	task, err := w.store.DequeueTask(ctx, storage.UUIDToPgtype(w.config.TenantID), w.config.WorkflowNames, w.config.VisibilityTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to dequeue task: %w", err)
	}

	if task == nil {
		// No tasks available
		return nil
	}

	// Check context again before processing
	if ctx.Err() != nil {
		// Context cancelled, return task to queue by not deleting it
		return ctx.Err()
	}

	// Process task based on type
	var processErr error
	switch TaskType(task.TaskType) {
	case TaskTypeWorkflow:
		processErr = w.processWorkflowTask(ctx, task)
	case TaskTypeActivity:
		processErr = w.processActivityTask(ctx, task)
	case TaskTypeTimer:
		processErr = w.processTimerTask(ctx, task)
	default:
		processErr = fmt.Errorf("unknown task type: %s", task.TaskType)
	}

	if processErr != nil {
		// Check if error is due to context cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Task failed - will be retried after visibility timeout
		return fmt.Errorf("task processing failed: %w", processErr)
	}

	// Task completed - remove from queue
	if err := w.store.DeleteTask(ctx, task.WorkflowName, storage.UUIDToPgtype(w.config.TenantID), task.ID); err != nil {
		// Don't report error if context was cancelled
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to delete task: %w", err)
	}

	return nil
}

// processWorkflowTask executes a workflow task.
func (w *Worker) processWorkflowTask(ctx context.Context, task *storage.TaskQueueModel) error {
	fmt.Printf("Processing workflow task: %s\n", storage.PgtypeToUUID(task.WorkflowID))

	workflowID := storage.PgtypeToUUID(task.WorkflowID)
	tenantID := storage.PgtypeToUUID(task.TenantID)

	// Load workflow from database
	wfModel, err := w.store.GetWorkflow(ctx, task.WorkflowName, task.TenantID, task.WorkflowID)
	if err != nil {
		return fmt.Errorf("failed to load workflow: %w", err)
	}

	// Extract base workflow name (remove shard suffix)
	baseName := ExtractBaseWorkflowName(wfModel.Name)

	// Get workflow definition from registry using base name (thread-safe)
	workflowEntry, ok := globalWorkflowRegistry.get(baseName)
	if !ok {
		return fmt.Errorf("workflow %s not registered", baseName)
	}

	// Execute workflow using type-erased execution
	return w.executeWorkflow(ctx, wfModel.Name, workflowID, tenantID, wfModel, workflowEntry)
}

// processActivityTask executes an activity task.
func (w *Worker) processActivityTask(ctx context.Context, task *storage.TaskQueueModel) error {
	workflowID := storage.PgtypeToUUID(task.WorkflowID)
	tenantID := storage.PgtypeToUUID(task.TenantID)

	fmt.Printf("Processing activity task for workflow: %s\n", workflowID)

	// Parse task data to get activity ID
	var taskData struct {
		ActivityID string `json:"activity_id"`
	}
	if err := json.Unmarshal(task.TaskData, &taskData); err != nil {
		return fmt.Errorf("failed to parse task data: %w", err)
	}

	activityID, err := uuid.Parse(taskData.ActivityID)
	if err != nil {
		return fmt.Errorf("invalid activity ID: %w", err)
	}

	// Load activity from database
	actModel, err := w.store.GetActivity(ctx, task.WorkflowName, task.TenantID, storage.UUIDToPgtype(activityID))
	if err != nil {
		return fmt.Errorf("failed to load activity: %w", err)
	}
	if actModel == nil {
		return fmt.Errorf("activity not found: %s", activityID)
	}

	// Get activity definition from registry (thread-safe)
	activityEntry, ok := globalActivityRegistry.get(actModel.Name)
	if !ok {
		return fmt.Errorf("activity %s not registered", actModel.Name)
	}

	// Update status to running
	if err := w.store.UpdateActivityStatus(ctx, task.WorkflowName, task.TenantID, actModel.ID, string(ActivityStatusRunning)); err != nil {
		return fmt.Errorf("failed to update activity status: %w", err)
	}

	// Execute activity
	output, execErr := w.executeActivity(ctx, workflowID, tenantID, activityID, actModel, activityEntry)

	if execErr != nil {
		// Check if error is terminal
		if IsTerminalError(execErr) {
			// Terminal error - don't retry, mark as failed
			actModel.Status = string(ActivityStatusFailed)
			actModel.Error = execErr.Error()
			if err := w.store.UpdateActivityFailed(ctx, actModel); err != nil {
				return fmt.Errorf("failed to update activity as failed: %w", err)
			}

			// Enqueue workflow task to handle failure
			if err := w.enqueueWorkflowTask(ctx, tenantID, workflowID, task.WorkflowName); err != nil {
				return fmt.Errorf("failed to enqueue workflow task: %w", err)
			}

			return nil
		}

		// Retryable error - calculate backoff and schedule retry
		actModel.Attempt++
		actModel.Error = execErr.Error()

		// Get retry policy from activity definition
		retryPolicy := w.getActivityRetryPolicy(activityEntry)

		// Check if max attempts exceeded
		if actModel.Attempt >= retryPolicy.MaxAttempts {
			actModel.Status = string(ActivityStatusFailed)
			if err := w.store.UpdateActivityFailed(ctx, actModel); err != nil {
				return fmt.Errorf("failed to update activity as failed: %w", err)
			}

			// Enqueue workflow task to handle failure
			if err := w.enqueueWorkflowTask(ctx, tenantID, workflowID, task.WorkflowName); err != nil {
				return fmt.Errorf("failed to enqueue workflow task: %w", err)
			}

			return nil
		}

		// Calculate next retry time
		backoffDuration := w.calculateBackoff(retryPolicy, actModel.Attempt)
		nextRetry := time.Now().Add(backoffDuration)
		actModel.NextRetryAt = &nextRetry
		actModel.BackoffMs = backoffDuration.Milliseconds()
		actModel.Status = string(ActivityStatusScheduled)

		if err := w.store.UpdateActivityFailed(ctx, actModel); err != nil {
			return fmt.Errorf("failed to update activity for retry: %w", err)
		}

		// Re-enqueue activity task with new visibility timeout
		retryTask := &storage.TaskQueueModel{
			ID:                storage.UUIDToPgtype(uuid.New()),
			TenantID:          task.TenantID,
			WorkflowID:        task.WorkflowID,
			WorkflowName:      task.WorkflowName,
			TaskType:          string(TaskTypeActivity),
			TaskData:          task.TaskData,
			VisibilityTimeout: nextRetry,
		}
		if err := w.store.EnqueueTask(ctx, retryTask, nil); err != nil {
			return fmt.Errorf("failed to enqueue retry task: %w", err)
		}

		return nil
	}

	// Activity succeeded - save output
	if err := w.store.UpdateActivityComplete(ctx, task.WorkflowName, task.TenantID, actModel.ID, output); err != nil {
		return fmt.Errorf("failed to update activity as completed: %w", err)
	}

	// Enqueue workflow task to resume execution
	if err := w.enqueueWorkflowTask(ctx, tenantID, workflowID, task.WorkflowName); err != nil {
		return fmt.Errorf("failed to enqueue workflow task: %w", err)
	}

	return nil
}

// processTimerTask handles a timer that has fired.
func (w *Worker) processTimerTask(ctx context.Context, task *storage.TaskQueueModel) error {
	workflowID := storage.PgtypeToUUID(task.WorkflowID)
	tenantID := storage.PgtypeToUUID(task.TenantID)

	fmt.Printf("Processing timer task for workflow: %s\n", workflowID)

	// Parse task data to get timer ID
	var taskData struct {
		TimerID string `json:"timer_id"`
	}
	if err := json.Unmarshal(task.TaskData, &taskData); err != nil {
		return fmt.Errorf("failed to parse task data: %w", err)
	}

	timerID, err := uuid.Parse(taskData.TimerID)
	if err != nil {
		return fmt.Errorf("invalid timer ID: %w", err)
	}

	// Mark timer as fired
	if err := w.store.MarkTimerFired(ctx, task.WorkflowName, task.TenantID, storage.UUIDToPgtype(timerID)); err != nil {
		return fmt.Errorf("failed to mark timer as fired: %w", err)
	}

	// Enqueue workflow task to resume execution
	if err := w.enqueueWorkflowTask(ctx, tenantID, workflowID, task.WorkflowName); err != nil {
		return fmt.Errorf("failed to enqueue workflow task: %w", err)
	}

	return nil
}

// executeWorkflow executes a workflow with the given definition.
// This implements the full workflow execution with deterministic replay.
func (w *Worker) executeWorkflow(
	ctx context.Context,
	workflowName string,
	workflowID uuid.UUID,
	tenantID uuid.UUID,
	wfModel *storage.WorkflowModel,
	workflowEntry *WorkflowRegistryEntry,
) error {
	// Update status to running
	if err := w.store.UpdateWorkflowStatus(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), string(StatusRunning)); err != nil {
		return fmt.Errorf("failed to update workflow status: %w", err)
	}

	// 1. Deserialize input using type info from registry
	input, err := registry.Deserialize(workflowEntry.inputType.Name, wfModel.Input)
	if err != nil {
		return w.failWorkflow(ctx, workflowName, tenantID, workflowID, fmt.Errorf("failed to deserialize input: %w", err))
	}

	// 2. Load activity results for replay
	activityResults := make(map[int]interface{})
	activityErrors := make(map[int]error)
	activities, err := w.store.GetActivitiesByWorkflow(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
	if err != nil {
		return fmt.Errorf("failed to load activities: %w", err)
	}

	for _, act := range activities {
		if act.Status == string(ActivityStatusCompleted) {
			// Deserialize activity output (thread-safe)
			actEntry, ok := globalActivityRegistry.get(act.Name)
			if !ok {
				continue // Skip unregistered activities
			}

			output, err := registry.Deserialize(actEntry.outputType.Name, act.Output)
			if err == nil {
				activityResults[act.SequenceNum] = output
			}
		} else if act.Status == string(ActivityStatusFailed) {
			// Store activity failure for replay
			activityErrors[act.SequenceNum] = fmt.Errorf("activity failed: %s", act.Error)
		}
	}

	// Load timer results for replay
	timerResults := make(map[int]time.Time)
	timers, err := w.store.GetTimersByWorkflow(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
	if err != nil {
		return fmt.Errorf("failed to load timers: %w", err)
	}

	for _, timer := range timers {
		if timer.Fired {
			// Store the fire time for fired timers so they skip on replay
			timerResults[timer.SequenceNum] = timer.FireAt
		}
	}

	// Load time results and random results for replay from history events
	timeResults := make(map[int]time.Time)
	randomResults := make(map[int][]byte)
	historyEvents, err := w.store.GetHistoryEvents(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
	if err != nil {
		return fmt.Errorf("failed to load history events: %w", err)
	}

	for _, event := range historyEvents {
		if event.EventType == string(EventTimeRecorded) {
			// Parse the recorded time from event data
			var eventData map[string]interface{}
			if err := json.Unmarshal(event.EventData, &eventData); err == nil {
				if timeStr, ok := eventData["recorded_time"].(string); ok {
					if recordedTime, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
						timeResults[event.SequenceNum] = recordedTime
					}
				}
			}
		} else if event.EventType == string(EventRandomGenerated) {
			// Parse the recorded random bytes from event data
			var eventData map[string]interface{}
			if err := json.Unmarshal(event.EventData, &eventData); err == nil {
				if randomBytesIface, ok := eventData["random_bytes"]; ok {
					// Random bytes are stored as []interface{} (JSON array of numbers)
					if randomBytesSlice, ok := randomBytesIface.([]interface{}); ok {
						randomBytes := make([]byte, len(randomBytesSlice))
						for i, b := range randomBytesSlice {
							if byteVal, ok := b.(float64); ok {
								randomBytes[i] = byte(byteVal)
							}
						}
						randomResults[event.SequenceNum] = randomBytes
					}
				}
			}
		}
	}

	// Load signal results for replay - store raw JSON, will be deserialized on demand
	signalResults := make(map[string]interface{})
	signals, err := w.store.GetSignalsByWorkflow(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID))
	if err != nil {
		return fmt.Errorf("failed to load signals: %w", err)
	}

	for _, sig := range signals {
		if sig.Consumed {
			// Store raw JSON payload for replay - will be deserialized to correct type in WaitForSignal
			signalResults[sig.SignalName] = sig.Payload
		}
	}

	// 3. Execute workflow with pause function
	var pauseErr error
	pauseFunc := func() error {
		pauseErr = ErrWorkflowPaused
		panic(ErrWorkflowPaused)
	}

	// 4. Execute workflow function with panic/recover
	var output interface{}
	var finalSequenceNum int
	var execErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Check if it's our expected pause
				if err, ok := r.(error); ok && err == ErrWorkflowPaused {
					// Workflow paused for activity/timer/signal - this is normal
					// The activity/timer/signal has been scheduled
					pauseErr = ErrWorkflowPaused
				} else {
					// Unexpected panic - mark workflow as failed
					panicErr := fmt.Errorf("workflow panicked: %v", r)
					if err := w.failWorkflow(ctx, workflowName, tenantID, workflowID, panicErr); err != nil {
						fmt.Printf("Failed to mark workflow as failed: %v\n", err)
					}
				}
			}
		}()

		// Execute workflow directly using the type-erased execute function
		// Returns: output, finalSequenceNum, error
		execCtx := &workflowExecutionContext{
			Ctx:             ctx,
			WorkflowID:      workflowID,
			TenantID:        tenantID,
			Input:           input,
			SequenceNum:     wfModel.SequenceNum,
			WorkflowName:    wfModel.Name,
			Store:           w.store,
			PauseFunc:       pauseFunc,
			ActivityResults: activityResults,
			ActivityErrors:  activityErrors,
			TimerResults:    timerResults,
			TimeResults:     timeResults,
			RandomResults:   randomResults,
			SignalResults:   signalResults,
		}
		output, finalSequenceNum, execErr = workflowEntry.execute(execCtx)
	}()

	// Check if workflow was paused
	if pauseErr == ErrWorkflowPaused {
		// Workflow paused - save current sequence number
		// The execute function returns the updated sequenceNum
		currentSequenceNum := finalSequenceNum

		// Serialize activity results to save
		activityResultsJSON, err := json.Marshal(activityResults)
		if err != nil {
			return fmt.Errorf("failed to serialize activity results: %w", err)
		}

		if err := w.store.UpdateWorkflowSequence(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), currentSequenceNum, activityResultsJSON); err != nil {
			return fmt.Errorf("failed to update workflow sequence: %w", err)
		}
		return nil
	}

	// 5. Workflow completed - check for error
	if execErr != nil {
		return w.failWorkflow(ctx, workflowName, tenantID, workflowID, execErr)
	}

	// 6. Serialize and save output
	outputData, err := json.Marshal(output)
	if err != nil {
		return w.failWorkflow(ctx, workflowName, tenantID, workflowID, fmt.Errorf("failed to serialize output: %w", err))
	}

	if err := w.store.UpdateWorkflowComplete(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), outputData); err != nil {
		return fmt.Errorf("failed to update workflow as completed: %w", err)
	}

	return nil
}

// failWorkflow marks a workflow as failed.
func (w *Worker) failWorkflow(ctx context.Context, workflowName string, tenantID, workflowID uuid.UUID, err error) error {
	if updateErr := w.store.UpdateWorkflowFailed(ctx, workflowName, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(workflowID), err.Error()); updateErr != nil {
		return fmt.Errorf("failed to mark workflow as failed: %w", updateErr)
	}
	return err
}

// enqueueWorkflowTask creates a workflow task to resume execution.
func (w *Worker) enqueueWorkflowTask(ctx context.Context, tenantID, workflowID uuid.UUID, workflowName string) error {
	taskID := uuid.New()
	task := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(taskID),
		TenantID:          storage.UUIDToPgtype(tenantID),
		WorkflowID:        storage.UUIDToPgtype(workflowID),
		WorkflowName:      workflowName,
		TaskType:          string(TaskTypeWorkflow),
		VisibilityTimeout: time.Now(),
	}
	return w.store.EnqueueTask(ctx, task, nil)
}

// executeActivity executes an activity using reflection to call the Execute method.
func (w *Worker) executeActivity(
	ctx context.Context,
	workflowID uuid.UUID,
	tenantID uuid.UUID,
	activityID uuid.UUID,
	actModel *storage.ActivityModel,
	activityEntry *ActivityRegistryEntry,
) (json.RawMessage, error) {
	// Deserialize input using type info from registry
	input, err := registry.Deserialize(activityEntry.inputType.Name, actModel.Input)
	if err != nil {
		return nil, NewTerminalError(fmt.Errorf("failed to deserialize input: %w", err))
	}

	// Create activity context
	activityCtx := NewActivityContext(ctx, workflowID, activityID, tenantID, actModel.Attempt)

	// Execute activity directly using the type-erased execute function
	output, err := activityEntry.execute(activityCtx.ctx, input)
	if err != nil {
		return nil, err
	}

	// Serialize output
	outputData, err := json.Marshal(output)
	if err != nil {
		return nil, NewTerminalError(fmt.Errorf("failed to serialize output: %w", err))
	}

	return outputData, nil
}

// getActivityRetryPolicy extracts retry policy from activity definition.
func (w *Worker) getActivityRetryPolicy(activityEntry *ActivityRegistryEntry) RetryPolicy {
	return activityEntry.retryPolicy
}

// calculateBackoff calculates the backoff duration for retry.
func (w *Worker) calculateBackoff(policy RetryPolicy, attempt int) time.Duration {
	// Exponential backoff: initialInterval * (backoffFactor ^ (attempt - 1))
	backoff := float64(policy.InitialInterval) * math.Pow(policy.BackoffFactor, float64(attempt-1))

	// Cap at max interval
	if backoff > float64(policy.MaxInterval) {
		backoff = float64(policy.MaxInterval)
	}

	// Add jitter: ±jitter%
	if policy.Jitter > 0 {
		jitterAmount := backoff * policy.Jitter
		jitter := (rand.Float64()*2 - 1) * jitterAmount // Random value between -jitterAmount and +jitterAmount
		backoff += jitter
	}

	// Ensure positive
	if backoff < 0 {
		backoff = float64(policy.InitialInterval)
	}

	return time.Duration(backoff)
}
