package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateWorkflow creates a new workflow in the database.
func (s *Store) CreateWorkflow(ctx context.Context, wf *WorkflowModel, tx interface{}) error {
	query := `
		INSERT INTO workflows (
			id, tenant_id, name, version, status, input, output, 
			error, sequence_num, activity_results
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				wf.ID, wf.TenantID, wf.Name, wf.Version, wf.Status,
				wf.Input, wf.Output, wf.Error, wf.SequenceNum, wf.ActivityResults,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			wf.ID, wf.TenantID, wf.Name, wf.Version, wf.Status,
			wf.Input, wf.Output, wf.Error, wf.SequenceNum, wf.ActivityResults,
		)
	}

	return err
}

// GetWorkflow retrieves a workflow by ID.
func (s *Store) GetWorkflow(ctx context.Context, tenantID, workflowID pgtype.UUID) (*WorkflowModel, error) {
	query := `
		SELECT id, tenant_id, name, version, status, input, output, 
		       error, sequence_num, activity_results, updated_at
		FROM workflows
		WHERE tenant_id = $1 AND id = $2
	`

	wf := &WorkflowModel{}
	err := s.pool.QueryRow(ctx, query, tenantID, workflowID).Scan(
		&wf.ID, &wf.TenantID, &wf.Name, &wf.Version, &wf.Status,
		&wf.Input, &wf.Output, &wf.Error, &wf.SequenceNum,
		&wf.ActivityResults, &wf.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("workflow not found")
	}
	if err != nil {
		return nil, err
	}

	return wf, nil
}

// UpdateWorkflowStatus updates the workflow status.
func (s *Store) UpdateWorkflowStatus(ctx context.Context, tenantID, workflowID pgtype.UUID, status string) error {
	query := `
		UPDATE workflows
		SET status = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, status, tenantID, workflowID)
	return err
}

// UpdateWorkflowComplete marks workflow as completed with output.
func (s *Store) UpdateWorkflowComplete(ctx context.Context, tenantID, workflowID pgtype.UUID, output json.RawMessage) error {
	query := `
		UPDATE workflows
		SET status = 'completed', output = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, output, tenantID, workflowID)
	return err
}

// UpdateWorkflowFailed marks workflow as failed with error.
func (s *Store) UpdateWorkflowFailed(ctx context.Context, tenantID, workflowID pgtype.UUID, errorMsg string) error {
	query := `
		UPDATE workflows
		SET status = 'failed', error = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, errorMsg, tenantID, workflowID)
	return err
}

// UpdateWorkflowSequence updates the workflow sequence number and activity results.
func (s *Store) UpdateWorkflowSequence(ctx context.Context, tenantID, workflowID pgtype.UUID, seqNum int, activityResults json.RawMessage) error {
	query := `
		UPDATE workflows
		SET sequence_num = $1, activity_results = $2, updated_at = NOW()
		WHERE tenant_id = $3 AND id = $4
	`

	_, err := s.pool.Exec(ctx, query, seqNum, activityResults, tenantID, workflowID)
	return err
}

// ListWorkflows lists workflows for a tenant with optional status filter.
func (s *Store) ListWorkflows(ctx context.Context, tenantID pgtype.UUID, status string, limit int) ([]*WorkflowModel, error) {
	var query string
	var args []interface{}

	if status != "" {
		query = `
			SELECT id, tenant_id, name, version, status, input, output, 
			       error, sequence_num, activity_results, updated_at
			FROM workflows
			WHERE tenant_id = $1 AND status = $2
			ORDER BY id DESC
			LIMIT $3
		`
		args = []interface{}{tenantID, status, limit}
	} else {
		query = `
			SELECT id, tenant_id, name, version, status, input, output, 
			       error, sequence_num, activity_results, updated_at
			FROM workflows
			WHERE tenant_id = $1
			ORDER BY id DESC
			LIMIT $2
		`
		args = []interface{}{tenantID, limit}
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workflows []*WorkflowModel
	for rows.Next() {
		wf := &WorkflowModel{}
		err := rows.Scan(
			&wf.ID, &wf.TenantID, &wf.Name, &wf.Version, &wf.Status,
			&wf.Input, &wf.Output, &wf.Error, &wf.SequenceNum,
			&wf.ActivityResults, &wf.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, wf)
	}

	return workflows, rows.Err()
}
