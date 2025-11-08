package storage

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateActivity creates a new activity in the database.
func (s *Store) CreateActivity(ctx context.Context, act *ActivityModel, tx interface{}) error {
	query := `
		INSERT INTO activities (
			id, tenant_id, workflow_id, name, sequence_num, status, 
			input, output, error, attempt, next_retry_at, backoff_ms
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				act.ID, act.TenantID, act.WorkflowID, act.Name, act.SequenceNum,
				act.Status, act.Input, act.Output, act.Error, act.Attempt,
				act.NextRetryAt, act.BackoffMs,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			act.ID, act.TenantID, act.WorkflowID, act.Name, act.SequenceNum,
			act.Status, act.Input, act.Output, act.Error, act.Attempt,
			act.NextRetryAt, act.BackoffMs,
		)
	}

	return err
}

// GetActivity retrieves an activity by ID.
func (s *Store) GetActivity(ctx context.Context, tenantID, activityID pgtype.UUID) (*ActivityModel, error) {
	query := `
		SELECT id, tenant_id, workflow_id, name, sequence_num, status, 
		       input, output, error, attempt, next_retry_at, backoff_ms, updated_at
		FROM activities
		WHERE tenant_id = $1 AND id = $2
	`

	act := &ActivityModel{}
	err := s.pool.QueryRow(ctx, query, tenantID, activityID).Scan(
		&act.ID, &act.TenantID, &act.WorkflowID, &act.Name, &act.SequenceNum,
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
func (s *Store) UpdateActivityStatus(ctx context.Context, tenantID, activityID pgtype.UUID, status string) error {
	query := `
		UPDATE activities
		SET status = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, status, tenantID, activityID)
	return err
}

// UpdateActivityComplete marks activity as completed with output.
func (s *Store) UpdateActivityComplete(ctx context.Context, tenantID, activityID pgtype.UUID, output json.RawMessage) error {
	query := `
		UPDATE activities
		SET status = 'completed', output = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, output, tenantID, activityID)
	return err
}

// UpdateActivityFailed marks activity as failed with error and retry info.
func (s *Store) UpdateActivityFailed(ctx context.Context, act *ActivityModel) error {
	query := `
		UPDATE activities
		SET status = $1, error = $2, attempt = $3, 
		    next_retry_at = $4, backoff_ms = $5, updated_at = NOW()
		WHERE tenant_id = $6 AND id = $7
	`

	_, err := s.pool.Exec(ctx, query,
		act.Status, act.Error, act.Attempt,
		act.NextRetryAt, act.BackoffMs,
		act.TenantID, act.ID,
	)
	return err
}

// GetActivitiesByWorkflow retrieves all activities for a workflow.
func (s *Store) GetActivitiesByWorkflow(ctx context.Context, tenantID, workflowID pgtype.UUID) ([]*ActivityModel, error) {
	query := `
		SELECT id, tenant_id, workflow_id, name, sequence_num, status, 
		       input, output, error, attempt, next_retry_at, backoff_ms, updated_at
		FROM activities
		WHERE tenant_id = $1 AND workflow_id = $2
		ORDER BY sequence_num ASC
	`

	rows, err := s.pool.Query(ctx, query, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []*ActivityModel
	for rows.Next() {
		act := &ActivityModel{}
		err := rows.Scan(
			&act.ID, &act.TenantID, &act.WorkflowID, &act.Name, &act.SequenceNum,
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
