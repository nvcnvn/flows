package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateActivity creates a new activity in the database.
func (s *Store) CreateActivity(ctx context.Context, act *ActivityModel, tx interface{}) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (
			workflow_name, id, tenant_id, workflow_id, name, sequence_num, status, 
			input, output, error, attempt, next_retry_at, backoff_ms
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, s.tableName("activities"))

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				act.WorkflowName, act.ID, act.TenantID, act.WorkflowID, act.Name, act.SequenceNum,
				act.Status, act.Input, act.Output, act.Error, act.Attempt,
				act.NextRetryAt, act.BackoffMs,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			act.WorkflowName, act.ID, act.TenantID, act.WorkflowID, act.Name, act.SequenceNum,
			act.Status, act.Input, act.Output, act.Error, act.Attempt,
			act.NextRetryAt, act.BackoffMs,
		)
	}

	return err
}

// GetActivity retrieves an activity by ID.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetActivity(ctx context.Context, workflowName string, tenantID, activityID pgtype.UUID) (*ActivityModel, error) {
	query := fmt.Sprintf(`
		SELECT workflow_name, id, tenant_id, workflow_id, name, sequence_num, status, 
		       input, output, error, attempt, next_retry_at, backoff_ms, updated_at
		FROM %s
		WHERE workflow_name = $1 AND tenant_id = $2 AND id = $3
	`, s.tableName("activities"))

	act := &ActivityModel{}
	err := s.pool.QueryRow(ctx, query, workflowName, tenantID, activityID).Scan(
		&act.WorkflowName, &act.ID, &act.TenantID, &act.WorkflowID, &act.Name, &act.SequenceNum,
		&act.Status, &act.Input, &act.Output, &act.Error, &act.Attempt,
		&act.NextRetryAt, &act.BackoffMs, &act.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return act, nil
}

// UpdateActivityStatus updates the activity status.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateActivityStatus(ctx context.Context, workflowName string, tenantID, activityID pgtype.UUID, status string) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = $1, updated_at = NOW()
		WHERE workflow_name = $2 AND tenant_id = $3 AND id = $4
	`, s.tableName("activities"))

	_, err := s.pool.Exec(ctx, query, status, workflowName, tenantID, activityID)
	return err
}

// UpdateActivityComplete marks activity as completed with output.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateActivityComplete(ctx context.Context, workflowName string, tenantID, activityID pgtype.UUID, output json.RawMessage) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = 'completed', output = $1, updated_at = NOW()
		WHERE workflow_name = $2 AND tenant_id = $3 AND id = $4
	`, s.tableName("activities"))

	_, err := s.pool.Exec(ctx, query, output, workflowName, tenantID, activityID)
	return err
}

// UpdateActivityFailed marks activity as failed with error and retry info.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateActivityFailed(ctx context.Context, act *ActivityModel) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = $1, error = $2, attempt = $3, 
		    next_retry_at = $4, backoff_ms = $5, updated_at = NOW()
		WHERE workflow_name = $6 AND tenant_id = $7 AND id = $8
	`, s.tableName("activities"))

	_, err := s.pool.Exec(ctx, query,
		act.Status, act.Error, act.Attempt,
		act.NextRetryAt, act.BackoffMs,
		act.WorkflowName, act.TenantID, act.ID,
	)
	return err
}

// GetActivitiesByWorkflow retrieves all activities for a workflow.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetActivitiesByWorkflow(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID) ([]*ActivityModel, error) {
	query := fmt.Sprintf(`
		SELECT workflow_name, id, tenant_id, workflow_id, name, sequence_num, status, 
		       input, output, error, attempt, next_retry_at, backoff_ms, updated_at
		FROM %s
		WHERE workflow_name = $1 AND tenant_id = $2 AND workflow_id = $3
		ORDER BY sequence_num ASC
	`, s.tableName("activities"))

	rows, err := s.pool.Query(ctx, query, workflowName, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []*ActivityModel
	for rows.Next() {
		act := &ActivityModel{}
		err := rows.Scan(
			&act.WorkflowName, &act.ID, &act.TenantID, &act.WorkflowID, &act.Name, &act.SequenceNum,
			&act.Status, &act.Input, &act.Output, &act.Error, &act.Attempt,
			&act.NextRetryAt, &act.BackoffMs, &act.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		activities = append(activities, act)
	}

	return activities, rows.Err()
}
