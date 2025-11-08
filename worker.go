package flows

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows/internal/storage"
)

// Worker polls and executes workflow tasks.
type Worker struct {
	store  *storage.Store
	config WorkerConfig
	stopCh chan struct{}
	doneCh chan struct{}
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

	return &Worker{
		store:  storage.NewStore(pool),
		config: config,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Run starts the worker. Blocks until Stop is called.
func (w *Worker) Run(ctx context.Context) error {
	defer close(w.doneCh)

	// Start worker goroutines
	for i := 0; i < w.config.Concurrency; i++ {
		go w.workerLoop(ctx)
	}

	// Wait for stop signal
	<-w.stopCh

	// TODO: Wait for in-flight tasks to complete
	return nil
}

// Stop gracefully stops the worker.
func (w *Worker) Stop() {
	close(w.stopCh)
	<-w.doneCh
}

// workerLoop polls for tasks and processes them.
func (w *Worker) workerLoop(ctx context.Context) {
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			if err := w.pollAndProcess(ctx); err != nil {
				// Log error (in production, use proper logging)
				fmt.Printf("worker error: %v\n", err)
			}
		}
	}
}

// pollAndProcess polls for a task and processes it.
func (w *Worker) pollAndProcess(ctx context.Context) error {
	// Add tenant ID to context
	ctx = WithTenantID(ctx, w.config.TenantID)

	// Dequeue task
	task, err := w.store.DequeueTask(ctx, storage.UUIDToPgtype(w.config.TenantID), w.config.WorkflowNames, w.config.VisibilityTimeout)
	if err != nil {
		return fmt.Errorf("failed to dequeue task: %w", err)
	}

	if task == nil {
		// No tasks available
		return nil
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
		// Task failed - will be retried after visibility timeout
		return fmt.Errorf("task processing failed: %w", processErr)
	}

	// Task completed - remove from queue
	if err := w.store.DeleteTask(ctx, storage.UUIDToPgtype(w.config.TenantID), task.ID); err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}

	return nil
}

// processWorkflowTask executes a workflow task.
func (w *Worker) processWorkflowTask(ctx context.Context, task *storage.TaskQueueModel) error {
	// TODO: Implement workflow execution using executor
	fmt.Printf("Processing workflow task: %s\n", storage.PgtypeToUUID(task.WorkflowID))
	return nil
}

// processActivityTask executes an activity task.
func (w *Worker) processActivityTask(ctx context.Context, task *storage.TaskQueueModel) error {
	// TODO: Implement activity execution using executor
	fmt.Printf("Processing activity task: %s\n", storage.PgtypeToUUID(task.WorkflowID))
	return nil
}

// processTimerTask handles a timer that has fired.
func (w *Worker) processTimerTask(ctx context.Context, task *storage.TaskQueueModel) error {
	// TODO: Implement timer processing
	fmt.Printf("Processing timer task: %s\n", storage.PgtypeToUUID(task.WorkflowID))
	return nil
}
