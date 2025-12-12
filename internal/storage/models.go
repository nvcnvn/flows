package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoreConfig contains configuration for the Store.
type StoreConfig struct {
	Pool   *pgxpool.Pool
	Schema string // Optional schema name (e.g., "myapp" → tables become "myapp.workflows")
}

// Store provides database operations for workflows.
type Store struct {
	pool   *pgxpool.Pool
	schema string
}

// NewStore creates a new storage instance.
func NewStore(pool *pgxpool.Pool, schema string) *Store {
	return &Store{pool: pool, schema: schema}
}

// Pool returns the underlying connection pool.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// tableName returns the schema-qualified table name.
// If no schema is configured, returns the base table name.
func (s *Store) tableName(base string) string {
	if s.schema == "" {
		return base
	}
	return s.schema + "." + base
}

// WorkflowModel represents a workflow in the database.
type WorkflowModel struct {
	ID              pgtype.UUID
	TenantID        pgtype.UUID
	Name            string
	Version         int
	Status          string
	Input           json.RawMessage
	Output          json.RawMessage
	Error           string
	SequenceNum     int
	ActivityResults json.RawMessage
	UpdatedAt       time.Time
}

// ActivityModel represents an activity in the database.
type ActivityModel struct {
	WorkflowName string // Shard key
	WorkflowID   pgtype.UUID
	ID           pgtype.UUID
	TenantID     pgtype.UUID
	Name         string
	SequenceNum  int
	Status       string
	Input        json.RawMessage
	Output       json.RawMessage
	Error        string
	Attempt      int
	NextRetryAt  *time.Time
	BackoffMs    int64
	UpdatedAt    time.Time
}

// HistoryEventModel represents a history event in the database.
type HistoryEventModel struct {
	WorkflowName string // Shard key
	WorkflowID   pgtype.UUID
	ID           pgtype.UUID
	TenantID     pgtype.UUID
	SequenceNum  int
	EventType    string
	EventData    json.RawMessage
}

// TimerModel represents a timer in the database.
type TimerModel struct {
	WorkflowName string // Shard key
	WorkflowID   pgtype.UUID
	ID           pgtype.UUID
	TenantID     pgtype.UUID
	SequenceNum  int
	FireAt       time.Time
	Fired        bool
}

// SignalModel represents a signal in the database.
type SignalModel struct {
	WorkflowName string // Shard key
	WorkflowID   pgtype.UUID
	ID           pgtype.UUID
	TenantID     pgtype.UUID
	SignalName   string
	Payload      json.RawMessage
	Consumed     bool
}

// TaskQueueModel represents a task in the queue.
type TaskQueueModel struct {
	ID                pgtype.UUID
	TenantID          pgtype.UUID
	WorkflowID        pgtype.UUID
	WorkflowName      string
	TaskType          string
	TaskData          json.RawMessage
	VisibilityTimeout time.Time
}

// DLQModel represents a dead letter queue entry.
type DLQModel struct {
	WorkflowName      string // Shard key
	ID                pgtype.UUID
	WorkflowID        pgtype.UUID
	TenantID          pgtype.UUID
	WorkflowVersion   int
	Input             json.RawMessage
	Error             string
	Attempt           int
	Metadata          json.RawMessage
	RerunAsWorkflowID *pgtype.UUID
}

// Executor interface for database operations (supports both pool and tx).
type Executor interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (interface{}, error)
	Query(ctx context.Context, sql string, args ...interface{}) (interface{}, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) interface{}
}
