package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows/internal/registry"
	"github.com/nvcnvn/flows/internal/storage"
)

// Workflow represents a workflow definition with typed input and output.
type Workflow[In, Out any] struct {
	name       string
	version    int
	fn         func(*Context[In]) (*Out, error)
	inputType  registry.TypeInfo
	outputType registry.TypeInfo
}

// WorkflowRegistryEntry stores both the workflow definition and a factory function
// to create properly typed contexts without reflection.
type WorkflowRegistryEntry struct {
	workflow       interface{}                                                                                                                                                                                                       // *Workflow[In, Out]
	inputType      registry.TypeInfo                                                                                                                                                                                                 // Type information for input
	outputType     registry.TypeInfo                                                                                                                                                                                                 // Type information for output
	contextFactory func(context.Context, uuid.UUID, uuid.UUID, interface{}, int, string, *storage.Store, func() error, map[int]interface{}, map[int]error, map[int]time.Time, map[int]time.Time, map[string]interface{}) interface{} // Factory to create Context[In]
}

// WorkflowRegistry stores registered workflows for execution.
type WorkflowRegistry struct {
	workflows map[string]*WorkflowRegistryEntry // name -> registry entry
}

var globalWorkflowRegistry = &WorkflowRegistry{
	workflows: make(map[string]*WorkflowRegistryEntry),
}

// New creates a new workflow definition.
func New[In, Out any](name string, version int, fn func(*Context[In]) (*Out, error)) *Workflow[In, Out] {
	// Register input and output types
	inputType := registry.Register[In]()
	outputType := registry.Register[Out]()

	wf := &Workflow[In, Out]{
		name:       name,
		version:    version,
		fn:         fn,
		inputType:  inputType,
		outputType: outputType,
	}

	// Create a factory function that knows how to create Context[In]
	contextFactory := func(
		ctx context.Context,
		workflowID uuid.UUID,
		tenantID uuid.UUID,
		input interface{},
		sequenceNum int,
		workflowName string,
		store *storage.Store,
		pauseFunc func() error,
		activityResults map[int]interface{},
		activityErrors map[int]error,
		timerResults map[int]time.Time,
		timeResults map[int]time.Time,
		signalResults map[string]interface{},
	) interface{} {
		// Cast input to the correct type
		typedInput, ok := input.(*In)
		if !ok {
			// This should never happen if deserialization worked correctly
			panic(fmt.Sprintf("input type mismatch: expected *%s, got %T", inputType.Name, input))
		}

		// Create deterministic random generator
		seed := int64(workflowID.ID())
		rnd := rand.New(rand.NewSource(seed))

		// Return properly typed Context[In]
		return &Context[In]{
			ctx:             ctx,
			workflowID:      workflowID,
			tenantID:        tenantID,
			input:           typedInput,
			sequenceNum:     sequenceNum,
			workflowName:    workflowName,
			store:           store,
			activityResults: activityResults,
			activityErrors:  activityErrors,
			timerResults:    timerResults,
			timeResults:     timeResults,
			signalResults:   signalResults,
			rnd:             rnd,
			pauseFunc:       pauseFunc,
		}
	}

	// Register workflow in global registry with its factory
	globalWorkflowRegistry.workflows[name] = &WorkflowRegistryEntry{
		workflow:       wf,
		inputType:      inputType,
		outputType:     outputType,
		contextFactory: contextFactory,
	}

	return wf
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
	ctx          context.Context
	workflowID   uuid.UUID
	tenantID     uuid.UUID
	input        *T
	sequenceNum  int
	workflowName string
	store        *storage.Store

	// Replay state
	activityResults map[int]interface{}
	activityErrors  map[int]error
	timerResults    map[int]time.Time
	timeResults     map[int]time.Time // Recorded times for deterministic Time() calls
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
		timeResults:     make(map[int]time.Time),
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

	// Check if activity failed (replay)
	if err, ok := c.activityErrors[currentSeq]; ok {
		return nil, err
	}

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

	// No cached result - need to schedule activity and pause workflow
	// Serialize activity input
	_, inputData, err := registry.Serialize(input)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize activity input: %w", err)
	}

	// Create activity record
	activityID := uuid.New()
	actModel := &storage.ActivityModel{
		ID:          storage.UUIDToPgtype(activityID),
		TenantID:    storage.UUIDToPgtype(c.tenantID),
		WorkflowID:  storage.UUIDToPgtype(c.workflowID),
		Name:        activity.Name(),
		SequenceNum: currentSeq,
		Status:      string(ActivityStatusScheduled),
		Input:       inputData,
		Attempt:     0,
	}

	if err := c.store.CreateActivity(c.ctx, actModel, nil); err != nil {
		return nil, fmt.Errorf("failed to create activity: %w", err)
	}

	// Enqueue activity task
	taskID := uuid.New()
	taskData, _ := json.Marshal(map[string]string{
		"activity_id": activityID.String(),
	})
	taskModel := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(taskID),
		TenantID:          storage.UUIDToPgtype(c.tenantID),
		WorkflowID:        storage.UUIDToPgtype(c.workflowID),
		WorkflowName:      c.workflowName,
		TaskType:          string(TaskTypeActivity),
		TaskData:          taskData,
		VisibilityTimeout: time.Now(),
	}

	if err := c.store.EnqueueTask(c.ctx, taskModel, nil); err != nil {
		return nil, fmt.Errorf("failed to enqueue activity task: %w", err)
	}

	// Now pause workflow execution
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

	// Timer not fired yet - create timer and schedule task
	timerID := uuid.New()
	fireAt := time.Now().Add(duration)

	timerModel := &storage.TimerModel{
		ID:          storage.UUIDToPgtype(timerID),
		TenantID:    storage.UUIDToPgtype(c.tenantID),
		WorkflowID:  storage.UUIDToPgtype(c.workflowID),
		SequenceNum: currentSeq,
		FireAt:      fireAt,
		Fired:       false,
	}

	if err := c.store.CreateTimer(c.ctx, timerModel, nil); err != nil {
		return fmt.Errorf("failed to create timer: %w", err)
	}

	// Enqueue timer task
	taskID := uuid.New()
	taskData, _ := json.Marshal(map[string]string{
		"timer_id": timerID.String(),
	})
	taskModel := &storage.TaskQueueModel{
		ID:                storage.UUIDToPgtype(taskID),
		TenantID:          storage.UUIDToPgtype(c.tenantID),
		WorkflowID:        storage.UUIDToPgtype(c.workflowID),
		WorkflowName:      c.workflowName,
		TaskType:          string(TaskTypeTimer),
		TaskData:          taskData,
		VisibilityTimeout: fireAt,
	}

	if err := c.store.EnqueueTask(c.ctx, taskModel, nil); err != nil {
		return fmt.Errorf("failed to enqueue timer task: %w", err)
	}

	// Now pause workflow
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

// Time returns the current time deterministically.
// During replay, returns the recorded time from the first execution.
// During first execution, records the current time in workflow history.
func (c *Context[T]) Time() time.Time {
	c.sequenceNum++
	currentSeq := c.sequenceNum

	// Check if we have a cached time (replay)
	if recordedTime, ok := c.timeResults[currentSeq]; ok {
		return recordedTime
	}

	// First execution - record current time
	now := time.Now()

	// Create history event to record the time
	eventData, _ := json.Marshal(map[string]interface{}{
		"recorded_time": now,
	})

	eventID := uuid.New()
	historyEvent := &storage.HistoryEventModel{
		ID:          storage.UUIDToPgtype(eventID),
		TenantID:    storage.UUIDToPgtype(c.tenantID),
		WorkflowID:  storage.UUIDToPgtype(c.workflowID),
		SequenceNum: currentSeq,
		EventType:   string(EventTimeRecorded),
		EventData:   eventData,
	}

	if err := c.store.CreateHistoryEvent(c.ctx, historyEvent, nil); err != nil {
		// If we can't record the time, fall back to using the current time
		// This maintains workflow progress even if history recording fails
		return now
	}

	// Cache for current execution
	c.timeResults[currentSeq] = now

	return now
}

// UUIDv7 generates a deterministic time-ordered UUID based on recorded time.
// Uses the Time() primitive to ensure deterministic timestamps during replay.
func (c *Context[T]) UUIDv7() uuid.UUID {
	// Get deterministic time - this will increment sequence internally
	recordedTime := c.Time()

	// Extract timestamp in milliseconds for UUID v7
	unixMs := recordedTime.UnixMilli()

	// Create UUID v7 with deterministic timestamp and random bits from workflow's RNG
	// UUID v7 format: 48 bits timestamp + 12 bits version/variant + 62 bits random
	var uuidBytes [16]byte

	// 48-bit timestamp (milliseconds since epoch)
	uuidBytes[0] = byte(unixMs >> 40)
	uuidBytes[1] = byte(unixMs >> 32)
	uuidBytes[2] = byte(unixMs >> 24)
	uuidBytes[3] = byte(unixMs >> 16)
	uuidBytes[4] = byte(unixMs >> 8)
	uuidBytes[5] = byte(unixMs)

	// Fill remaining bytes with deterministic random data
	for i := 6; i < 16; i++ {
		uuidBytes[i] = byte(c.rnd.Int31n(256))
	}

	// Set version (7) in bits 48-51
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x70

	// Set variant (10) in bits 64-65
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80

	return uuid.UUID(uuidBytes)
}

// WaitForSignal waits for an external signal with the given name.
// Returns the typed payload when signal arrives.
// This is a top-level function to support generics (Go doesn't allow generic methods).
func WaitForSignal[T, P any](c *Context[T], signalName string) (*P, error) {
	// Check if signal already received (replay)
	if cached, ok := c.signalResults[signalName]; ok {
		// Deserialize from raw JSON (stored during replay setup)
		var payload P
		if cached != nil {
			// Check if it's already deserialized (pointer to P)
			if typedPayload, ok := cached.(*P); ok {
				return typedPayload, nil
			}
			// Otherwise it's raw JSON bytes
			if rawJSON, ok := cached.(json.RawMessage); ok {
				if len(rawJSON) > 0 {
					if err := json.Unmarshal(rawJSON, &payload); err != nil {
						return nil, fmt.Errorf("failed to deserialize cached signal payload: %w", err)
					}
					return &payload, nil
				}
			}
		}
		return nil, nil
	}

	// Check if signal is available in database
	signal, err := c.store.GetSignal(c.ctx, storage.UUIDToPgtype(c.tenantID), storage.UUIDToPgtype(c.workflowID), signalName)
	if err != nil {
		return nil, fmt.Errorf("failed to check for signal: %w", err)
	}

	if signal != nil {
		// Signal available - consume it and return payload
		if err := c.store.ConsumeSignal(c.ctx, storage.UUIDToPgtype(c.tenantID), signal.ID); err != nil {
			return nil, fmt.Errorf("failed to consume signal: %w", err)
		}

		// Deserialize payload
		var payload P
		if len(signal.Payload) > 0 {
			if err := json.Unmarshal(signal.Payload, &payload); err != nil {
				return nil, fmt.Errorf("failed to deserialize signal payload: %w", err)
			}
		}

		// Cache the raw JSON for future replays
		c.signalResults[signalName] = json.RawMessage(signal.Payload)

		return &payload, nil
	}

	// Signal not available yet - pause workflow
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

// SetTimeResult sets a recorded time (used during replay).
func (c *Context[T]) SetTimeResult(seq int, recordedTime time.Time) {
	c.timeResults[seq] = recordedTime
}

// SetSignalResult sets a cached signal payload (used during replay).
func (c *Context[T]) SetSignalResult(signalName string, payload interface{}) {
	c.signalResults[signalName] = payload
}

// Execution represents a handle to a running workflow execution.
type Execution[Out any] struct {
	id         uuid.UUID
	workflowID uuid.UUID
	store      *storage.Store
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
	// Poll for workflow completion
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	tenantID := MustGetTenantID(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			// Check workflow status
			wf, err := e.store.GetWorkflow(ctx, storage.UUIDToPgtype(tenantID), storage.UUIDToPgtype(e.workflowID))
			if err != nil {
				return nil, err
			}

			switch WorkflowStatus(wf.Status) {
			case StatusCompleted:
				// Deserialize output
				if len(wf.Output) == 0 {
					return nil, nil
				}
				var result Out
				if err := json.Unmarshal(wf.Output, &result); err != nil {
					return nil, fmt.Errorf("failed to deserialize output: %w", err)
				}
				return &result, nil

			case StatusFailed:
				if wf.Error != "" {
					return nil, fmt.Errorf("workflow failed: %s", wf.Error)
				}
				return nil, fmt.Errorf("workflow failed with unknown error")

			case StatusRunning, StatusPending:
				// Continue polling
				continue

			default:
				return nil, fmt.Errorf("unknown workflow status: %s", wf.Status)
			}
		}
	}
}
