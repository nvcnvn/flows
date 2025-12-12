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
	query := fmt.Sprintf(`
		INSERT INTO %s (
			name, id, tenant_id, version, status, input, output, 
			error, sequence_num, activity_results
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, s.tableName("workflows"))

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				wf.Name, wf.ID, wf.TenantID, wf.Version, wf.Status,
				wf.Input, wf.Output, wf.Error, wf.SequenceNum, wf.ActivityResults,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			wf.Name, wf.ID, wf.TenantID, wf.Version, wf.Status,
			wf.Input, wf.Output, wf.Error, wf.SequenceNum, wf.ActivityResults,
		)
	}

	return err
}

// GetWorkflow retrieves a workflow by ID.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetWorkflow(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID, tx ...interface{}) (*WorkflowModel, error) {
	query := fmt.Sprintf(`
		SELECT id, tenant_id, name, version, status, input, output, 
		       error, sequence_num, activity_results, updated_at
		FROM %s
		WHERE name = $1 AND tenant_id = $2 AND id = $3
	`, s.tableName("workflows"))

	wf := &WorkflowModel{}
	var err error

	if len(tx) > 0 && tx[0] != nil {
		if pgxTx, ok := tx[0].(pgx.Tx); ok {
			err = pgxTx.QueryRow(ctx, query, workflowName, tenantID, workflowID).Scan(
				&wf.ID, &wf.TenantID, &wf.Name, &wf.Version, &wf.Status,
				&wf.Input, &wf.Output, &wf.Error, &wf.SequenceNum,
				&wf.ActivityResults, &wf.UpdatedAt,
			)
		}
	} else {
		err = s.pool.QueryRow(ctx, query, workflowName, tenantID, workflowID).Scan(
			&wf.ID, &wf.TenantID, &wf.Name, &wf.Version, &wf.Status,
			&wf.Input, &wf.Output, &wf.Error, &wf.SequenceNum,
			&wf.ActivityResults, &wf.UpdatedAt,
		)
	}

	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("workflow not found")
	}
	if err != nil {
		return nil, err
	}

	return wf, nil
}

// GetWorkflowByID retrieves a workflow by ID only, without workflow_name.
// This results in a broadcast query across all shards (less efficient).
// Use GetWorkflow() when workflow_name is known for better performance.
func (s *Store) GetWorkflowByID(ctx context.Context, tenantID, workflowID pgtype.UUID) (*WorkflowModel, error) {
	query := fmt.Sprintf(`
		SELECT id, tenant_id, name, version, status, input, output, 
		       error, sequence_num, activity_results, updated_at
		FROM %s
		WHERE tenant_id = $1 AND id = $2
	`, s.tableName("workflows"))

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
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateWorkflowStatus(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID, status string) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = $1, updated_at = NOW()
		WHERE name = $2 AND tenant_id = $3 AND id = $4
	`, s.tableName("workflows"))

	_, err := s.pool.Exec(ctx, query, status, workflowName, tenantID, workflowID)
	return err
}

// UpdateWorkflowComplete marks workflow as completed with output.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateWorkflowComplete(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID, output json.RawMessage) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = 'completed', output = $1, updated_at = NOW()
		WHERE name = $2 AND tenant_id = $3 AND id = $4
	`, s.tableName("workflows"))

	_, err := s.pool.Exec(ctx, query, output, workflowName, tenantID, workflowID)
	return err
}

// UpdateWorkflowFailed marks workflow as failed with error.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateWorkflowFailed(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID, errorMsg string) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = 'failed', error = $1, updated_at = NOW()
		WHERE name = $2 AND tenant_id = $3 AND id = $4
	`, s.tableName("workflows"))

	_, err := s.pool.Exec(ctx, query, errorMsg, workflowName, tenantID, workflowID)
	return err
}

// UpdateWorkflowSequence updates the workflow sequence number and activity results.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateWorkflowSequence(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID, seqNum int, activityResults json.RawMessage) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET sequence_num = $1, activity_results = $2, updated_at = NOW()
		WHERE name = $3 AND tenant_id = $4 AND id = $5
	`, s.tableName("workflows"))

	_, err := s.pool.Exec(ctx, query, seqNum, activityResults, workflowName, tenantID, workflowID)
	return err
}

// ListWorkflows lists workflows for a tenant with optional status filter.
// workflow_name must be provided first for efficient shard routing in Citus.
// Note: This will only list workflows from a single shard. To list across all shards,
// call this function multiple times with different workflow names.
func (s *Store) ListWorkflows(ctx context.Context, workflowName string, tenantID pgtype.UUID, status string, limit int) ([]*WorkflowModel, error) {
	var query string
	var args []interface{}

	if status != "" {
		query = fmt.Sprintf(`
			SELECT id, tenant_id, name, version, status, input, output, 
			       error, sequence_num, activity_results, updated_at
			FROM %s
			WHERE name = $1 AND tenant_id = $2 AND status = $3
			ORDER BY id DESC
			LIMIT $4
		`, s.tableName("workflows"))
		args = []interface{}{workflowName, tenantID, status, limit}
	} else {
		query = fmt.Sprintf(`
			SELECT id, tenant_id, name, version, status, input, output, 
			       error, sequence_num, activity_results, updated_at
			FROM %s
			WHERE name = $1 AND tenant_id = $2
			ORDER BY id DESC
			LIMIT $3
		`, s.tableName("workflows"))
		args = []interface{}{workflowName, tenantID, limit}
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
