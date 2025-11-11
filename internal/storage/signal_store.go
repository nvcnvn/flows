package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateSignal creates a new signal in the database.
func (s *Store) CreateSignal(ctx context.Context, sig *SignalModel, tx interface{}) error {
	query := `
		INSERT INTO signals (
			workflow_name, id, tenant_id, workflow_id, signal_name, payload, consumed
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				sig.WorkflowName, sig.ID, sig.TenantID, sig.WorkflowID, sig.SignalName,
				sig.Payload, sig.Consumed,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			sig.WorkflowName, sig.ID, sig.TenantID, sig.WorkflowID, sig.SignalName,
			sig.Payload, sig.Consumed,
		)
	}

	return err
}

// GetSignal retrieves an unconsumed signal for a workflow.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetSignal(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID, signalName string) (*SignalModel, error) {
	query := `
		SELECT workflow_name, id, tenant_id, workflow_id, signal_name, payload, consumed
		FROM signals
		WHERE workflow_name = $1 AND tenant_id = $2 AND workflow_id = $3 AND signal_name = $4 AND NOT consumed
		ORDER BY id ASC
		LIMIT 1
	`

	sig := &SignalModel{}
	err := s.pool.QueryRow(ctx, query, workflowName, tenantID, workflowID, signalName).Scan(
		&sig.WorkflowName, &sig.ID, &sig.TenantID, &sig.WorkflowID, &sig.SignalName,
		&sig.Payload, &sig.Consumed,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return sig, nil
}

// ConsumeSignal marks a signal as consumed.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) ConsumeSignal(ctx context.Context, workflowName string, tenantID, signalID pgtype.UUID) error {
	query := `
		UPDATE signals
		SET consumed = true
		WHERE workflow_name = $1 AND tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, workflowName, tenantID, signalID)
	return err
}

// GetSignalsByWorkflow retrieves all signals for a workflow.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetSignalsByWorkflow(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID) ([]*SignalModel, error) {
	query := `
		SELECT workflow_name, id, tenant_id, workflow_id, signal_name, payload, consumed
		FROM signals
		WHERE workflow_name = $1 AND tenant_id = $2 AND workflow_id = $3
		ORDER BY id ASC
	`

	rows, err := s.pool.Query(ctx, query, workflowName, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*SignalModel
	for rows.Next() {
		sig := &SignalModel{}
		err := rows.Scan(
			&sig.WorkflowName, &sig.ID, &sig.TenantID, &sig.WorkflowID, &sig.SignalName,
			&sig.Payload, &sig.Consumed,
		)
		if err != nil {
			return nil, err
		}
		signals = append(signals, sig)
	}

	return signals, rows.Err()
}

// CreateTimer creates a new timer in the database.
func (s *Store) CreateTimer(ctx context.Context, timer *TimerModel, tx interface{}) error {
	query := `
		INSERT INTO timers (
			workflow_name, id, tenant_id, workflow_id, sequence_num, fire_at, fired
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				timer.WorkflowName, timer.ID, timer.TenantID, timer.WorkflowID,
				timer.SequenceNum, timer.FireAt, timer.Fired,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			timer.WorkflowName, timer.ID, timer.TenantID, timer.WorkflowID,
			timer.SequenceNum, timer.FireAt, timer.Fired,
		)
	}

	return err
}

// GetFiredTimers retrieves timers that should fire.
// workflow_name must be provided first for efficient shard routing in Citus.
// Note: This will only check timers from a single shard. To check across all shards,
// call this function multiple times with different workflow names.
func (s *Store) GetFiredTimers(ctx context.Context, workflowName string, tenantID pgtype.UUID, limit int) ([]*TimerModel, error) {
	query := `
		SELECT workflow_name, id, tenant_id, workflow_id, sequence_num, fire_at, fired
		FROM timers
		WHERE workflow_name = $1 AND tenant_id = $2 AND NOT fired AND fire_at <= NOW()
		ORDER BY fire_at ASC
		LIMIT $3
	`

	rows, err := s.pool.Query(ctx, query, workflowName, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var timers []*TimerModel
	for rows.Next() {
		timer := &TimerModel{}
		err := rows.Scan(
			&timer.WorkflowName, &timer.ID, &timer.TenantID, &timer.WorkflowID,
			&timer.SequenceNum, &timer.FireAt, &timer.Fired,
		)
		if err != nil {
			return nil, err
		}
		timers = append(timers, timer)
	}

	return timers, rows.Err()
}

// MarkTimerFired marks a timer as fired.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) MarkTimerFired(ctx context.Context, workflowName string, tenantID, timerID pgtype.UUID) error {
	query := `
		UPDATE timers
		SET fired = true
		WHERE workflow_name = $1 AND tenant_id = $2 AND id = $3
	`

	_, err := s.pool.Exec(ctx, query, workflowName, tenantID, timerID)
	return err
}

// GetTimersByWorkflow retrieves all timers for a workflow.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetTimersByWorkflow(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID) ([]*TimerModel, error) {
	query := `
		SELECT workflow_name, id, tenant_id, workflow_id, sequence_num, fire_at, fired
		FROM timers
		WHERE workflow_name = $1 AND tenant_id = $2 AND workflow_id = $3
		ORDER BY sequence_num ASC
	`

	rows, err := s.pool.Query(ctx, query, workflowName, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var timers []*TimerModel
	for rows.Next() {
		timer := &TimerModel{}
		err := rows.Scan(
			&timer.WorkflowName, &timer.ID, &timer.TenantID, &timer.WorkflowID,
			&timer.SequenceNum, &timer.FireAt, &timer.Fired,
		)
		if err != nil {
			return nil, err
		}
		timers = append(timers, timer)
	}

	return timers, rows.Err()
}

// CreateHistoryEvent creates a history event.
func (s *Store) CreateHistoryEvent(ctx context.Context, event *HistoryEventModel, tx interface{}) error {
	query := `
		INSERT INTO history_events (
			workflow_name, id, tenant_id, workflow_id, sequence_num, event_type, event_data
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	var err error
	if tx != nil {
		if pgxTx, ok := tx.(pgx.Tx); ok {
			_, err = pgxTx.Exec(ctx, query,
				event.WorkflowName, event.ID, event.TenantID, event.WorkflowID,
				event.SequenceNum, event.EventType, event.EventData,
			)
		}
	} else {
		_, err = s.pool.Exec(ctx, query,
			event.WorkflowName, event.ID, event.TenantID, event.WorkflowID,
			event.SequenceNum, event.EventType, event.EventData,
		)
	}

	return err
}

// GetHistoryEvents retrieves history events for a workflow.
// workflow_name must be provided first for efficient shard routing in Citus.
func (s *Store) GetHistoryEvents(ctx context.Context, workflowName string, tenantID, workflowID pgtype.UUID) ([]*HistoryEventModel, error) {
	query := `
		SELECT workflow_name, id, tenant_id, workflow_id, sequence_num, event_type, event_data
		FROM history_events
		WHERE workflow_name = $1 AND tenant_id = $2 AND workflow_id = $3
		ORDER BY sequence_num ASC
	`

	rows, err := s.pool.Query(ctx, query, workflowName, tenantID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*HistoryEventModel
	for rows.Next() {
		event := &HistoryEventModel{}
		err := rows.Scan(
			&event.WorkflowName, &event.ID, &event.TenantID, &event.WorkflowID,
			&event.SequenceNum, &event.EventType, &event.EventData,
		)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, rows.Err()
}
