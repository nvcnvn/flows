package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateDLQEntry adds a workflow to the dead letter queue.
func (s *Store) CreateDLQEntry(ctx context.Context, dlq *DLQModel) error {
	query := `
		INSERT INTO dead_letter_queue (
			id, tenant_id, workflow_id, workflow_name, workflow_version,
			input, error, attempt, metadata, rerun_as_workflow_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err := s.pool.Exec(ctx, query,
		dlq.ID, dlq.TenantID, dlq.WorkflowID, dlq.WorkflowName,
		dlq.WorkflowVersion, dlq.Input, dlq.Error, dlq.Attempt,
		dlq.Metadata, dlq.RerunAsWorkflowID,
	)

	return err
}

// GetDLQEntry retrieves a DLQ entry by ID.
func (s *Store) GetDLQEntry(ctx context.Context, tenantID, dlqID pgtype.UUID) (*DLQModel, error) {
	query := `
		SELECT id, tenant_id, workflow_id, workflow_name, workflow_version,
		       input, error, attempt, metadata, rerun_as_workflow_id
		FROM dead_letter_queue
		WHERE tenant_id = $1 AND id = $2
	`

	dlq := &DLQModel{}
	err := s.pool.QueryRow(ctx, query, tenantID, dlqID).Scan(
		&dlq.ID, &dlq.TenantID, &dlq.WorkflowID, &dlq.WorkflowName,
		&dlq.WorkflowVersion, &dlq.Input, &dlq.Error, &dlq.Attempt,
		&dlq.Metadata, &dlq.RerunAsWorkflowID,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return dlq, nil
}

// ListDLQ lists dead letter queue entries for a tenant.
func (s *Store) ListDLQ(ctx context.Context, tenantID pgtype.UUID, limit int) ([]*DLQModel, error) {
	query := `
		SELECT id, tenant_id, workflow_id, workflow_name, workflow_version,
		       input, error, attempt, metadata, rerun_as_workflow_id
		FROM dead_letter_queue
		WHERE tenant_id = $1
		ORDER BY id DESC
		LIMIT $2
	`

	rows, err := s.pool.Query(ctx, query, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*DLQModel
	for rows.Next() {
		dlq := &DLQModel{}
		err := rows.Scan(
			&dlq.ID, &dlq.TenantID, &dlq.WorkflowID, &dlq.WorkflowName,
			&dlq.WorkflowVersion, &dlq.Input, &dlq.Error, &dlq.Attempt,
			&dlq.Metadata, &dlq.RerunAsWorkflowID,
		)
		if err != nil {
			return nil, err
		}
		entries = append(entries, dlq)
	}

	return entries, rows.Err()
}

// UpdateDLQRerun marks a DLQ entry as rerun with new workflow ID.
func (s *Store) UpdateDLQRerun(ctx context.Context, tenantID, dlqID, newWorkflowID pgtype.UUID) error {
	query := `
		UPDATE dead_letter_queue
		SET rerun_as_workflow_id = $1
		WHERE tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, newWorkflowID, tenantID, dlqID)
	return err
}
