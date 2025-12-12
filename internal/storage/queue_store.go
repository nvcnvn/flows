package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// EnqueueTask adds a task to the task queue.
func (s *Store) EnqueueTask(ctx context.Context, task *TaskQueueModel, tx interface{}) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (
			id, tenant_id, workflow_id, workflow_name, task_type, 
			task_data, visibility_timeout
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, s.tableName("task_queue"))

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
// Works across all tenants - the task contains the tenant_id.
func (s *Store) DequeueTask(ctx context.Context, workflowNames []string, visibilityTimeout time.Duration) (*TaskQueueModel, error) {
	// Try each workflow name separately for Citus compatibility
	// This ensures each query targets a single shard
	for _, workflowName := range workflowNames {
		task, err := s.tryDequeueTaskForWorkflow(ctx, workflowName, visibilityTimeout)
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
// Works across all tenants - returns the first available task regardless of tenant.
func (s *Store) tryDequeueTaskForWorkflow(ctx context.Context, workflowName string, visibilityTimeout time.Duration) (*TaskQueueModel, error) {
	query := fmt.Sprintf(`
		SELECT id, tenant_id, workflow_id, workflow_name, task_type, 
		       task_data, visibility_timeout
		FROM %s
		WHERE workflow_name = $1
		  AND visibility_timeout < NOW()
		ORDER BY id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, s.tableName("task_queue"))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if err := tx.Rollback(ctx); err != nil && err != pgx.ErrTxClosed {
			slog.Error("tryDequeueTaskForWorkflow Rollback error", "error", err)
		}
	}()

	task := &TaskQueueModel{}
	err = tx.QueryRow(ctx, query, workflowName).Scan(
		&task.ID, &task.TenantID, &task.WorkflowID, &task.WorkflowName,
		&task.TaskType, &task.TaskData, &task.VisibilityTimeout,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			// No tasks available - commit the transaction and return nil
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return nil, commitErr
			}
			committed = true
			return nil, nil
		}
		return nil, err
	}

	// Update visibility timeout
	updateQuery := fmt.Sprintf(`
		UPDATE %s 
		SET visibility_timeout = $1
		WHERE workflow_name = $2 AND id = $3
	`, s.tableName("task_queue"))
	newTimeout := time.Now().Add(visibilityTimeout)
	_, err = tx.Exec(ctx, updateQuery, newTimeout, task.WorkflowName, task.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true

	task.VisibilityTimeout = newTimeout
	return task, nil
}

// DeleteTask removes a task from the queue after successful processing.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) DeleteTask(ctx context.Context, workflowName string, tenantID, taskID pgtype.UUID) error {
	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE workflow_name = $1 AND tenant_id = $2 AND id = $3
	`, s.tableName("task_queue"))

	_, err := s.pool.Exec(ctx, query, workflowName, tenantID, taskID)
	return err
}

// GetQueueDepth returns the number of pending tasks for a tenant.
func (s *Store) GetQueueDepth(ctx context.Context, tenantID pgtype.UUID) (int, error) {
	query := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s
		WHERE tenant_id = $1 AND visibility_timeout < NOW()
	`, s.tableName("task_queue"))

	var count int
	err := s.pool.QueryRow(ctx, query, tenantID).Scan(&count)
	return count, err
}
