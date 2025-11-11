package storage

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// EnqueueTask adds a task to the task queue.
func (s *Store) EnqueueTask(ctx context.Context, task *TaskQueueModel, tx interface{}) error {
	query := `
		INSERT INTO task_queue (
			id, tenant_id, workflow_id, workflow_name, task_type, 
			task_data, visibility_timeout
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				task.ID, task.TenantID, task.WorkflowID, task.WorkflowName,
				task.TaskType, task.TaskData, task.VisibilityTimeout,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			task.ID, task.TenantID, task.WorkflowID, task.WorkflowName,
			task.TaskType, task.TaskData, task.VisibilityTimeout,
		)
	}

	return err
}

// DequeueTask claims a task from the queue using SELECT FOR UPDATE SKIP LOCKED.
// In Citus, FOR UPDATE requires an equality filter on the distribution column.
// We try each workflow_name separately to ensure single-shard queries.
func (s *Store) DequeueTask(ctx context.Context, tenantID pgtype.UUID, workflowNames []string, visibilityTimeout time.Duration) (*TaskQueueModel, error) {
	// Try each workflow name separately for Citus compatibility
	// This ensures each query targets a single shard
	for _, workflowName := range workflowNames {
		task, err := s.tryDequeueTaskForWorkflow(ctx, tenantID, workflowName, visibilityTimeout)
		if err != nil {
			return nil, err
		}
		if task != nil {
			return task, nil
		}
	}

	// No tasks found in any workflow
	return nil, nil
}

// tryDequeueTaskForWorkflow attempts to dequeue a task for a specific workflow name.
// This uses equality on workflow_name for Citus single-shard routing with FOR UPDATE.
func (s *Store) tryDequeueTaskForWorkflow(ctx context.Context, tenantID pgtype.UUID, workflowName string, visibilityTimeout time.Duration) (*TaskQueueModel, error) {
	query := `
		SELECT id, tenant_id, workflow_id, workflow_name, task_type, 
		       task_data, visibility_timeout
		FROM task_queue
		WHERE workflow_name = $1
		  AND tenant_id = $2
		  AND visibility_timeout < NOW()
		ORDER BY id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		rollbackErr := tx.Rollback(ctx)
		if err == nil && rollbackErr != nil {
			slog.Error("tryDequeueTaskForWorkflow Rollback error", "error", rollbackErr)
		}
	}()

	task := &TaskQueueModel{}
	err = tx.QueryRow(ctx, query, workflowName, tenantID).Scan(
		&task.ID, &task.TenantID, &task.WorkflowID, &task.WorkflowName,
		&task.TaskType, &task.TaskData, &task.VisibilityTimeout,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Update visibility timeout
	updateQuery := `
		UPDATE task_queue 
		SET visibility_timeout = $1
		WHERE workflow_name = $2 AND id = $3
	`
	newTimeout := time.Now().Add(visibilityTimeout)
	_, err = tx.Exec(ctx, updateQuery, newTimeout, task.WorkflowName, task.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	task.VisibilityTimeout = newTimeout
	return task, nil
}

// DeleteTask removes a task from the queue after successful processing.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) DeleteTask(ctx context.Context, workflowName string, tenantID, taskID pgtype.UUID) error {
	query := `
		DELETE FROM task_queue
		WHERE workflow_name = $1 AND tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, workflowName, tenantID, taskID)
	return err
}

// GetQueueDepth returns the number of pending tasks for a tenant.
func (s *Store) GetQueueDepth(ctx context.Context, tenantID pgtype.UUID) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM task_queue
		WHERE tenant_id = $1 AND visibility_timeout < NOW()
	`

	var count int
	err := s.pool.QueryRow(ctx, query, tenantID).Scan(&count)
	return count, err
}
