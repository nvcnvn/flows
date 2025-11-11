package flows

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
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

// workflowExecutionContext contains all parameters needed to execute a workflow.
type workflowExecutionContext struct {
	Ctx             context.Context
	WorkflowID      uuid.UUID
	TenantID        uuid.UUID
	Input           interface{}
	SequenceNum     int
	WorkflowName    string
	Store           *storage.Store
	PauseFunc       func() error
	ActivityResults map[int]interface{}
	ActivityErrors  map[int]error
	TimerResults    map[int]time.Time
	TimeResults     map[int]time.Time
	RandomResults   map[int][]byte
	SignalResults   map[string]interface{}
}

// WorkflowRegistryEntry stores workflow metadata and execution function.
type WorkflowRegistryEntry struct {
	inputType  registry.TypeInfo                                         // Type information for input
	outputType registry.TypeInfo                                         // Type information for output
	execute    func(*workflowExecutionContext) (interface{}, int, error) // Type-erased execution function - returns (output, finalSequenceNum, error)
}

// WorkflowRegistry stores registered workflows for execution.
type WorkflowRegistry struct {
	mu        sync.RWMutex
	workflows map[string]*WorkflowRegistryEntry // name -> registry entry
}

var globalWorkflowRegistry = &WorkflowRegistry{
	workflows: make(map[string]*WorkflowRegistryEntry),
}

// register adds a workflow to the registry (thread-safe).
func (r *WorkflowRegistry) register(name string, entry *WorkflowRegistryEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workflows[name] = entry
}

// get retrieves a workflow from the registry (thread-safe).
func (r *WorkflowRegistry) get(name string) (*WorkflowRegistryEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.workflows[name]
	return entry, ok
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

	// Create a type-erased execution function that constructs Context[In] and executes the workflow
	executeFunc := func(execCtx *workflowExecutionContext) (interface{}, int, error) {
		// Cast input to the correct type
		typedInput, ok := execCtx.Input.(*In)
		if !ok {
			// This should never happen if deserialization worked correctly
			return nil, 0, fmt.Errorf("input type mismatch: expected *%s, got %T", inputType.Name, execCtx.Input)
		}

		// Construct Context[In] directly
		wfCtx := &Context[In]{
			ctx:             execCtx.Ctx,
			workflowID:      execCtx.WorkflowID,
			tenantID:        execCtx.TenantID,
			input:           typedInput,
			sequenceNum:     execCtx.SequenceNum,
			workflowName:    execCtx.WorkflowName,
			store:           execCtx.Store,
			activityResults: execCtx.ActivityResults,
			activityErrors:  execCtx.ActivityErrors,
			timerResults:    execCtx.TimerResults,
			timeResults:     execCtx.TimeResults,
			randomResults:   execCtx.RandomResults,
			signalResults:   execCtx.SignalResults,
			pauseFunc:       execCtx.PauseFunc,
		}

		// Execute workflow function directly (no reflection needed)
		output, err := fn(wfCtx)
		// Return output, final sequence number, and error
		return output, wfCtx.sequenceNum, err
	}

	// Register workflow in global registry with execution function (thread-safe)
	globalWorkflowRegistry.register(name, &WorkflowRegistryEntry{
		inputType:  inputType,
		outputType: outputType,
		execute:    executeFunc,
	})

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
	randomResults   map[int][]byte    // Recorded random bytes for deterministic Random() calls
	signalResults   map[string]interface{}

	// Pause function (set by executor)
	pauseFunc func() error
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

// Random generates cryptographically secure random bytes and stores them in the database.
// During replay, returns the cached random bytes from the first execution.
// This provides true random generation while maintaining deterministic replay.
// Returns the requested number of random bytes.
func (c *Context[T]) Random(numBytes int) ([]byte, error) {
	c.sequenceNum++
	currentSeq := c.sequenceNum

	// Check if we have cached random bytes (replay)
	if cachedBytes, ok := c.randomResults[currentSeq]; ok {
		return cachedBytes, nil
	}

	// First execution - generate cryptographically secure random bytes
	randomBytes := make([]byte, numBytes)
	if _, err := cryptorand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Create history event to record the random bytes
	eventData, _ := json.Marshal(map[string]interface{}{
		"random_bytes": randomBytes,
		"num_bytes":    numBytes,
	})

	eventID := uuid.New()
	historyEvent := &storage.HistoryEventModel{
		ID:          storage.UUIDToPgtype(eventID),
		TenantID:    storage.UUIDToPgtype(c.tenantID),
		WorkflowID:  storage.UUIDToPgtype(c.workflowID),
		SequenceNum: currentSeq,
		EventType:   string(EventRandomGenerated),
		EventData:   eventData,
	}

	if err := c.store.CreateHistoryEvent(c.ctx, historyEvent, nil); err != nil {
		// If we can't record the random bytes, return error
		// This ensures we don't generate different random values on replay
		return nil, fmt.Errorf("failed to record random bytes: %w", err)
	}

	// Cache for current execution
	c.randomResults[currentSeq] = randomBytes

	return randomBytes, nil
}

// RandomInt generates a cryptographically secure random integer in the range [0, max).
// Uses Random() internally to ensure deterministic replay.
func (c *Context[T]) RandomInt(max int) (int, error) {
	if max <= 0 {
		return 0, fmt.Errorf("max must be positive")
	}

	// Calculate number of bytes needed
	numBytes := 4 // Use 4 bytes for int32 range
	if max > 1<<24 {
		numBytes = 8 // Use 8 bytes for larger ranges
	}

	randomBytes, err := c.Random(numBytes)
	if err != nil {
		return 0, err
	}

	// Convert bytes to uint64
	var value uint64
	for i := 0; i < len(randomBytes) && i < 8; i++ {
		value |= uint64(randomBytes[i]) << (i * 8)
	}

	// Return value in range [0, max)
	return int(value % uint64(max)), nil
}

// RandomInt63 generates a cryptographically secure random int64 in the range [0, 2^63).
// Uses Random() internally to ensure deterministic replay.
func (c *Context[T]) RandomInt63() (int64, error) {
	randomBytes, err := c.Random(8)
	if err != nil {
		return 0, err
	}

	// Convert bytes to int64 and mask to get positive value
	var value int64
	for i := 0; i < 8; i++ {
		value |= int64(randomBytes[i]) << (i * 8)
	}

	// Ensure positive by masking the sign bit
	return value & 0x7FFFFFFFFFFFFFFF, nil
}

// RandomFloat64 generates a cryptographically secure random float64 in the range [0.0, 1.0).
// Uses Random() internally to ensure deterministic replay.
func (c *Context[T]) RandomFloat64() (float64, error) {
	randomBytes, err := c.Random(8)
	if err != nil {
		return 0, err
	}

	// Convert bytes to uint64
	var value uint64
	for i := 0; i < 8; i++ {
		value |= uint64(randomBytes[i]) << (i * 8)
	}

	// Convert to float64 in range [0.0, 1.0)
	// Use 53 bits of precision (standard for float64)
	return float64(value&((1<<53)-1)) / float64(1<<53), nil
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

// UUIDv7 generates a deterministic time-ordered UUID based on recorded time and random bytes.
// Uses the Time() primitive to ensure deterministic timestamps during replay,
// and Random() to ensure cryptographically secure random bits that replay deterministically.
func (c *Context[T]) UUIDv7() (uuid.UUID, error) {
	// Get deterministic time - this will increment sequence internally
	recordedTime := c.Time()

	// Extract timestamp in milliseconds for UUID v7
	unixMs := recordedTime.UnixMilli()

	// Get cryptographically secure random bytes - this will also increment sequence
	// UUID v7 format: 48 bits timestamp + 12 bits version/variant + 62 bits random
	// We need 10 bytes of random data (bytes 6-15)
	randomBytes, err := c.Random(10)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to generate random bytes for UUID: %w", err)
	}

	var uuidBytes [16]byte

	// 48-bit timestamp (milliseconds since epoch)
	uuidBytes[0] = byte(unixMs >> 40)
	uuidBytes[1] = byte(unixMs >> 32)
	uuidBytes[2] = byte(unixMs >> 24)
	uuidBytes[3] = byte(unixMs >> 16)
	uuidBytes[4] = byte(unixMs >> 8)
	uuidBytes[5] = byte(unixMs)

	// Fill remaining bytes with cryptographically secure random data
	copy(uuidBytes[6:], randomBytes)

	// Set version (7) in bits 48-51
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x70

	// Set variant (10) in bits 64-65
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80

	return uuid.UUID(uuidBytes), nil
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

// SetRandomResult sets cached random bytes (used during replay).
func (c *Context[T]) SetRandomResult(seq int, randomBytes []byte) {
	c.randomResults[seq] = randomBytes
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
