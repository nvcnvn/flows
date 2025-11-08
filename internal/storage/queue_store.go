package storage

import (
	"context"
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
func (s *Store) DequeueTask(ctx context.Context, tenantID pgtype.UUID, workflowNames []string, visibilityTimeout time.Duration) (*TaskQueueModel, error) {
	query := `
		SELECT id, tenant_id, workflow_id, workflow_name, task_type, 
		       task_data, visibility_timeout
		FROM task_queue
		WHERE tenant_id = $1 
		  AND workflow_name = ANY($2)
		  AND visibility_timeout < NOW()
		ORDER BY id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	task := &TaskQueueModel{}
	err = tx.QueryRow(ctx, query, tenantID, workflowNames).Scan(
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
		WHERE id = $2
	`
	newTimeout := time.Now().Add(visibilityTimeout)
	_, err = tx.Exec(ctx, updateQuery, newTimeout, task.ID)
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
func (s *Store) DeleteTask(ctx context.Context, tenantID, taskID pgtype.UUID) error {
	query := `
		DELETE FROM task_queue
		WHERE tenant_id = $1 AND id = $2
	`

	_, err := s.pool.Exec(ctx, query, tenantID, taskID)
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
