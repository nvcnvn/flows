package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateDLQEntry adds a workflow to the dead letter queue.
func (s *Store) CreateDLQEntry(ctx context.Context, dlq *DLQModel) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (
			id, tenant_id, workflow_id, workflow_name, workflow_version,
			input, error, attempt, metadata, rerun_as_workflow_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, s.tableName("dead_letter_queue"))

	_, err := s.pool.Exec(ctx, query,
		dlq.ID, dlq.TenantID, dlq.WorkflowID, dlq.WorkflowName,
		dlq.WorkflowVersion, dlq.Input, dlq.Error, dlq.Attempt,
		dlq.Metadata, dlq.RerunAsWorkflowID,
	)

	return err
}

// GetDLQEntry retrieves a DLQ entry by ID.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetDLQEntry(ctx context.Context, workflowName string, tenantID, dlqID pgtype.UUID) (*DLQModel, error) {
	query := fmt.Sprintf(`
		SELECT id, tenant_id, workflow_id, workflow_name, workflow_version,
		       input, error, attempt, metadata, rerun_as_workflow_id
		FROM %s
		WHERE workflow_name = $1 AND tenant_id = $2 AND id = $3
	`, s.tableName("dead_letter_queue"))

	dlq := &DLQModel{}
	err := s.pool.QueryRow(ctx, query, workflowName, tenantID, dlqID).Scan(
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

// GetDLQEntryByID retrieves a DLQ entry by ID only, without workflow_name.
// This results in a broadcast query across all shards (less efficient).
// Use GetDLQEntry() when workflow_name is known for better performance.
func (s *Store) GetDLQEntryByID(ctx context.Context, tenantID, dlqID pgtype.UUID) (*DLQModel, error) {
	query := fmt.Sprintf(`
		SELECT id, tenant_id, workflow_id, workflow_name, workflow_version,
		       input, error, attempt, metadata, rerun_as_workflow_id
		FROM %s
		WHERE tenant_id = $1 AND id = $2
	`, s.tableName("dead_letter_queue"))

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
// workflow_name must be provided first for efficient shard routing in Citus.
// Note: This will only list entries from a single shard. To list across all shards,
// call this function multiple times with different workflow names.
func (s *Store) ListDLQ(ctx context.Context, workflowName string, tenantID pgtype.UUID, limit int) ([]*DLQModel, error) {
	query := fmt.Sprintf(`
		SELECT id, tenant_id, workflow_id, workflow_name, workflow_version,
		       input, error, attempt, metadata, rerun_as_workflow_id
		FROM %s
		WHERE workflow_name = $1 AND tenant_id = $2
		ORDER BY id DESC
		LIMIT $3
	`, s.tableName("dead_letter_queue"))

	rows, err := s.pool.Query(ctx, query, workflowName, tenantID, limit)
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
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) UpdateDLQRerun(ctx context.Context, workflowName string, tenantID, dlqID, newWorkflowID pgtype.UUID) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET rerun_as_workflow_id = $1
		WHERE workflow_name = $2 AND tenant_id = $3 AND id = $4
	`, s.tableName("dead_letter_queue"))

	_, err := s.pool.Exec(ctx, query, newWorkflowID, workflowName, tenantID, dlqID)
	return err
}
