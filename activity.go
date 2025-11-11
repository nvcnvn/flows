package flows

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows/internal/registry"
)

// Activity represents an activity definition with typed input and output.
type Activity[In, Out any] struct {
	name        string
	fn          func(context.Context, *In) (*Out, error)
	retryPolicy RetryPolicy
	inputType   registry.TypeInfo
	outputType  registry.TypeInfo
}

// ActivityRegistryEntry stores activity metadata and execution function.
type ActivityRegistryEntry struct {
	inputType   registry.TypeInfo
	outputType  registry.TypeInfo
	retryPolicy RetryPolicy
	execute     func(context.Context, interface{}) (interface{}, error) // Type-erased execution function
}

// ActivityRegistry stores registered activities for execution.
type ActivityRegistry struct {
	activities map[string]*ActivityRegistryEntry // name -> registry entry
}

var globalActivityRegistry = &ActivityRegistry{
	activities: make(map[string]*ActivityRegistryEntry),
}

// NewActivity creates a new activity definition.
func NewActivity[In, Out any](
	name string,
	fn func(context.Context, *In) (*Out, error),
	retryPolicy RetryPolicy,
) *Activity[In, Out] {
	// Register input and output types
	inputType := registry.Register[In]()
	outputType := registry.Register[Out]()

	act := &Activity[In, Out]{
		name:        name,
		fn:          fn,
		retryPolicy: retryPolicy,
		inputType:   inputType,
		outputType:  outputType,
	}

	// Create type-erased execution function
	executeFunc := func(ctx context.Context, input interface{}) (interface{}, error) {
		typedInput, ok := input.(*In)
		if !ok {
			return nil, NewTerminalError(fmt.Errorf("input type mismatch: expected *%s, got %T", inputType.Name, input))
		}
		return fn(ctx, typedInput)
	}

	// Register activity in global registry with execution function
	globalActivityRegistry.activities[name] = &ActivityRegistryEntry{
		inputType:   inputType,
		outputType:  outputType,
		retryPolicy: retryPolicy,
		execute:     executeFunc,
	}

	return act
}

// Name returns the activity name.
func (a *Activity[In, Out]) Name() string {
	return a.name
}

// RetryPolicy returns the activity's retry policy.
func (a *Activity[In, Out]) RetryPolicy() RetryPolicy {
	return a.retryPolicy
}

// InputType returns the input type info.
func (a *Activity[In, Out]) InputType() registry.TypeInfo {
	return a.inputType
}

// OutputType returns the output type info.
func (a *Activity[In, Out]) OutputType() registry.TypeInfo {
	return a.outputType
}

// Execute runs the activity function (used internally).
func (a *Activity[In, Out]) Execute(ctx context.Context, input *In) (*Out, error) {
	return a.fn(ctx, input)
}

// ActivityContext provides context and metadata for activity execution.
type ActivityContext struct {
	ctx        context.Context
	workflowID uuid.UUID
	activityID uuid.UUID
	tenantID   uuid.UUID
	attempt    int
}

// NewActivityContext creates a new activity context.
func NewActivityContext(
	ctx context.Context,
	workflowID uuid.UUID,
	activityID uuid.UUID,
	tenantID uuid.UUID,
	attempt int,
) *ActivityContext {
	return &ActivityContext{
		ctx:        ctx,
		workflowID: workflowID,
		activityID: activityID,
		tenantID:   tenantID,
		attempt:    attempt,
	}
}

// Context returns the underlying Go context.
func (ac *ActivityContext) Context() context.Context {
	return ac.ctx
}

// WorkflowID returns the parent workflow ID.
func (ac *ActivityContext) WorkflowID() uuid.UUID {
	return ac.workflowID
}

// ActivityID returns this activity's ID.
func (ac *ActivityContext) ActivityID() uuid.UUID {
	return ac.activityID
}

// TenantID returns the tenant ID.
func (ac *ActivityContext) TenantID() uuid.UUID {
	return ac.tenantID
}

// Attempt returns the current attempt number (1-indexed).
func (ac *ActivityContext) Attempt() int {
	return ac.attempt
}
