package flows

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tx represents a database transaction interface.
// Compatible with pgx.Tx for external transaction support.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

// WorkflowStatus represents the current state of a workflow.
type WorkflowStatus string

const (
	StatusPending   WorkflowStatus = "pending"
	StatusRunning   WorkflowStatus = "running"
	StatusCompleted WorkflowStatus = "completed"
	StatusFailed    WorkflowStatus = "failed"
)

// ActivityStatus represents the current state of an activity.
type ActivityStatus string

const (
	ActivityStatusScheduled ActivityStatus = "scheduled"
	ActivityStatusRunning   ActivityStatus = "running"
	ActivityStatusCompleted ActivityStatus = "completed"
	ActivityStatusFailed    ActivityStatus = "failed"
)

// TaskType represents the type of task in the queue.
type TaskType string

const (
	TaskTypeWorkflow TaskType = "workflow"
	TaskTypeActivity TaskType = "activity"
	TaskTypeTimer    TaskType = "timer"
)

// EventType represents the type of history event.
type EventType string

const (
	EventWorkflowStarted   EventType = "WORKFLOW_STARTED"
	EventWorkflowCompleted EventType = "WORKFLOW_COMPLETED"
	EventWorkflowFailed    EventType = "WORKFLOW_FAILED"
	EventActivityScheduled EventType = "ACTIVITY_SCHEDULED"
	EventActivityStarted   EventType = "ACTIVITY_STARTED"
	EventActivityCompleted EventType = "ACTIVITY_COMPLETED"
	EventActivityFailed    EventType = "ACTIVITY_FAILED"
	EventTimerScheduled    EventType = "TIMER_SCHEDULED"
	EventTimerFired        EventType = "TIMER_FIRED"
	EventSignalReceived    EventType = "SIGNAL_RECEIVED"
	EventSignalConsumed    EventType = "SIGNAL_CONSUMED"
	EventTimeRecorded      EventType = "TIME_RECORDED"
	EventRandomGenerated   EventType = "RANDOM_GENERATED"
)

// Error definitions
var (
	// ErrWorkflowPaused is used internally to pause workflow execution.
	ErrWorkflowPaused = errors.New("workflow paused")

	// ErrWorkflowNotFound indicates the workflow does not exist.
	ErrWorkflowNotFound = errors.New("workflow not found")

	// ErrSignalNotFound indicates the signal does not exist.
	ErrSignalNotFound = errors.New("signal not found")

	// ErrTimeout indicates an operation timed out.
	ErrTimeout = errors.New("timeout")

	// ErrNoTenantID indicates tenant ID is missing from context.
	ErrNoTenantID = errors.New("tenant ID not found in context")
)

// RetryableError wraps an error to indicate it should be retried.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string {
	return "retryable: " + e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// NewRetryableError creates a new retryable error.
func NewRetryableError(err error) error {
	return &RetryableError{Err: err}
}

// TerminalError wraps an error to indicate it should not be retried.
type TerminalError struct {
	Err error
}

func (e *TerminalError) Error() string {
	return "terminal: " + e.Err.Error()
}

func (e *TerminalError) Unwrap() error {
	return e.Err
}

// NewTerminalError creates a new terminal error.
func NewTerminalError(err error) error {
	return &TerminalError{Err: err}
}

// IsTerminalError checks if an error is terminal.
func IsTerminalError(err error) bool {
	var te *TerminalError
	return errors.As(err, &te)
}

// RetryPolicy defines how activities should be retried.
type RetryPolicy struct {
	InitialInterval time.Duration // Start with this delay
	MaxInterval     time.Duration // Cap delay at this value
	BackoffFactor   float64       // Exponential backoff multiplier
	MaxAttempts     int           // Give up after this many tries
	Jitter          float64       // Add ±% randomization to prevent thundering herd
}

// DefaultRetryPolicy provides sensible defaults.
var DefaultRetryPolicy = RetryPolicy{
	InitialInterval: 1 * time.Second,
	MaxInterval:     1 * time.Hour,
	BackoffFactor:   2.0,
	MaxAttempts:     10,
	Jitter:          0.1,
}

// WorkflowInfo contains metadata about a workflow execution.
type WorkflowInfo struct {
	ID       string
	TenantID string
	Name     string
	Version  int
	Status   WorkflowStatus
	Error    string
}

// ActivityInfo contains metadata about an activity execution.
type ActivityInfo struct {
	ID          string
	WorkflowID  string
	Name        string
	SequenceNum int
	Status      ActivityStatus
	Attempt     int
}

// NoInput is a placeholder for workflows or activities that don't take input.
type NoInput struct{}

// NoOutput is a placeholder for workflows or activities that don't return output.
type NoOutput struct{}

// WorkflowStatusInfo represents the status of a workflow execution.
type WorkflowStatusInfo struct {
	WorkflowID string
	Status     string
	Error      string
}
