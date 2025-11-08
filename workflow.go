package flows

import (
	"context"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows/internal/registry"
)

// Workflow represents a workflow definition with typed input and output.
type Workflow[In, Out any] struct {
	name       string
	version    int
	fn         func(*Context[In]) (*Out, error)
	inputType  registry.TypeInfo
	outputType registry.TypeInfo
}

// New creates a new workflow definition.
func New[In, Out any](name string, version int, fn func(*Context[In]) (*Out, error)) *Workflow[In, Out] {
	// Register input and output types
	inputType := registry.Register[In]()
	outputType := registry.Register[Out]()

	return &Workflow[In, Out]{
		name:       name,
		version:    version,
		fn:         fn,
		inputType:  inputType,
		outputType: outputType,
	}
}

// Name returns the workflow name.
func (w *Workflow[In, Out]) Name() string {
	return w.name
}

// Version returns the workflow version.
func (w *Workflow[In, Out]) Version() int {
	return w.version
}

// InputType returns the input type info.
func (w *Workflow[In, Out]) InputType() registry.TypeInfo {
	return w.inputType
}

// OutputType returns the output type info.
func (w *Workflow[In, Out]) OutputType() registry.TypeInfo {
	return w.outputType
}

// Execute runs the workflow function (used internally).
func (w *Workflow[In, Out]) Execute(ctx *Context[In]) (*Out, error) {
	return w.fn(ctx)
}

// Context provides deterministic primitives for workflow execution.
type Context[T any] struct {
	ctx         context.Context
	workflowID  uuid.UUID
	tenantID    uuid.UUID
	input       *T
	sequenceNum int

	// Replay state
	activityResults map[int]interface{}
	timerResults    map[int]time.Time
	signalResults   map[string]interface{}

	// Deterministic random
	rnd *rand.Rand

	// Pause function (set by executor)
	pauseFunc func() error
}

// newContext creates a new workflow context.
func newContext[T any](
	ctx context.Context,
	workflowID uuid.UUID,
	tenantID uuid.UUID,
	input *T,
	sequenceNum int,
	pauseFunc func() error,
) *Context[T] {
	// Create deterministic random generator
	seed := int64(workflowID.ID())
	rnd := rand.New(rand.NewSource(seed))

	return &Context[T]{
		ctx:             ctx,
		workflowID:      workflowID,
		tenantID:        tenantID,
		input:           input,
		sequenceNum:     sequenceNum,
		activityResults: make(map[int]interface{}),
		timerResults:    make(map[int]time.Time),
		signalResults:   make(map[string]interface{}),
		rnd:             rnd,
		pauseFunc:       pauseFunc,
	}
}

// Context returns the underlying Go context.
func (c *Context[T]) Context() context.Context {
	return c.ctx
}

// WorkflowID returns the workflow execution ID.
func (c *Context[T]) WorkflowID() uuid.UUID {
	return c.workflowID
}

// TenantID returns the tenant ID.
func (c *Context[T]) TenantID() uuid.UUID {
	return c.tenantID
}

// Input returns the workflow input.
func (c *Context[T]) Input() *T {
	return c.input
}

// ExecuteActivity executes an activity with typed input and output.
// During replay, returns cached result. During execution, schedules activity and pauses.
// This is a top-level function to support generics (Go doesn't allow generic methods).
func ExecuteActivity[T, In, Out any](c *Context[T], activity *Activity[In, Out], input *In) (*Out, error) {
	c.sequenceNum++
	currentSeq := c.sequenceNum

	// Check if we have a cached result (replay)
	if cached, ok := c.activityResults[currentSeq]; ok {
		if cached == nil {
			return nil, nil
		}
		result, ok := cached.(*Out)
		if !ok {
			return nil, ErrWorkflowPaused // Type mismatch, need re-execution
		}
		return result, nil
	}

	// No cached result - need to execute activity
	// Pause workflow execution
	if c.pauseFunc != nil {
		if err := c.pauseFunc(); err != nil {
			return nil, err
		}
	}

	// This code should never be reached during normal execution
	// as pauseFunc will panic with ErrWorkflowPaused
	return nil, ErrWorkflowPaused
}

// Sleep pauses workflow execution for the specified duration.
// Creates a timer and pauses until it fires.
func (c *Context[T]) Sleep(duration time.Duration) error {
	c.sequenceNum++
	currentSeq := c.sequenceNum

	// Check if timer already fired (replay)
	if _, ok := c.timerResults[currentSeq]; ok {
		return nil
	}

	// Timer not fired yet - pause workflow
	if c.pauseFunc != nil {
		if err := c.pauseFunc(); err != nil {
			return err
		}
	}

	return ErrWorkflowPaused
}

// Random returns a deterministic random number generator.
// Always returns the same sequence for the same workflow execution.
func (c *Context[T]) Random() *rand.Rand {
	return c.rnd
}

// UUIDv7 generates a deterministic UUID based on workflow ID and sequence.
func (c *Context[T]) UUIDv7() uuid.UUID {
	c.sequenceNum++

	// Generate deterministic UUID using workflow ID and sequence as seed
	seed := c.workflowID.String() + string(rune(c.sequenceNum))
	return uuid.NewSHA1(c.workflowID, []byte(seed))
}

// WaitForSignal waits for an external signal with the given name.
// Returns the typed payload when signal arrives.
// This is a top-level function to support generics (Go doesn't allow generic methods).
func WaitForSignal[T, P any](c *Context[T], signalName string) (*P, error) {
	// Check if signal already received (replay)
	if cached, ok := c.signalResults[signalName]; ok {
		if cached == nil {
			return nil, nil
		}
		payload, ok := cached.(*P)
		if !ok {
			return nil, ErrWorkflowPaused // Type mismatch
		}
		return payload, nil
	}

	// Signal not received yet - pause workflow
	if c.pauseFunc != nil {
		if err := c.pauseFunc(); err != nil {
			return nil, err
		}
	}

	return nil, ErrWorkflowPaused
}

// SetActivityResult sets a cached activity result (used during replay).
func (c *Context[T]) SetActivityResult(seq int, result interface{}) {
	c.activityResults[seq] = result
}

// SetTimerResult marks a timer as fired (used during replay).
func (c *Context[T]) SetTimerResult(seq int, firedAt time.Time) {
	c.timerResults[seq] = firedAt
}

// SetSignalResult sets a cached signal payload (used during replay).
func (c *Context[T]) SetSignalResult(signalName string, payload interface{}) {
	c.signalResults[signalName] = payload
}

// Execution represents a handle to a running workflow execution.
type Execution[Out any] struct {
	id         uuid.UUID
	workflowID uuid.UUID
	store      interface{} // Will be storage.Store
}

// ID returns the execution ID.
func (e *Execution[Out]) ID() uuid.UUID {
	return e.id
}

// WorkflowID returns the workflow ID.
func (e *Execution[Out]) WorkflowID() uuid.UUID {
	return e.workflowID
}

// Get waits for the workflow to complete and returns the result.
// Blocks until the workflow finishes or context is cancelled.
func (e *Execution[Out]) Get(ctx context.Context) (*Out, error) {
	// TODO: Implement polling or waiting mechanism
	// For now, return placeholder
	return nil, nil
}
