package flows

import (
	"context"
	"fmt"
	"sync"
)

type workflowRunner interface {
	workflowName() string
	codec() Codec
	concurrency() int
	run(ctx context.Context, wfCtx *Context, inputJSON []byte) (outputJSON []byte, err error)
}

type registeredWorkflow[I any, O any] struct {
	wf             Workflow[I, O]
	codecImpl      Codec
	concurrencyVal int
}

func (r registeredWorkflow[I, O]) workflowName() string { return r.wf.Name() }

func (r registeredWorkflow[I, O]) codec() Codec { return r.codecImpl }

func (r registeredWorkflow[I, O]) concurrency() int {
	if r.concurrencyVal <= 0 {
		return 1
	}
	return r.concurrencyVal
}

func (r registeredWorkflow[I, O]) run(ctx context.Context, wfCtx *Context, inputJSON []byte) ([]byte, error) {
	var in I
	if err := r.codecImpl.Unmarshal(inputJSON, &in); err != nil {
		return nil, err
	}

	out, err := r.wf.Run(ctx, wfCtx, &in)
	if err != nil {
		return nil, err
	}
	return r.codecImpl.Marshal(out)
}

// WorkflowOption configures workflow registration.
type WorkflowOption func(*workflowOptions)

type workflowOptions struct {
	codec       Codec
	concurrency int
}

// WithCodec sets a custom codec for the workflow. If not set, JSONCodec is used.
func WithCodec(codec Codec) WorkflowOption {
	return func(o *workflowOptions) {
		o.codec = codec
	}
}

// WithConcurrency sets the number of concurrent goroutines for this workflow.
// Default is 1. Each workflow type runs in its own goroutine pool.
func WithConcurrency(n int) WorkflowOption {
	return func(o *workflowOptions) {
		o.concurrency = n
	}
}

// Registry maps workflow names to registered handlers.
//
// Registration is type-safe; execution is dynamic (by workflow name from DB).
type Registry struct {
	mu        sync.RWMutex
	workflows map[string]workflowRunner

	cronMu        sync.RWMutex
	cronSchedules []cronEntry
}

// cronEntry holds a registered cron schedule.
type cronEntry struct {
	scheduleID   string
	workflowName string
	schedule     Schedule
	cronExpr     string
	inputJSON    []byte
}

func NewRegistry() *Registry {
	return &Registry{workflows: map[string]workflowRunner{}}
}

// Register registers a workflow with optional configuration.
//
// Go does not support type parameters on methods, so this is a package-level generic.
func Register[I any, O any](r *Registry, wf Workflow[I, O], opts ...WorkflowOption) {
	if err := register(r, wf, opts...); err != nil {
		panic(err)
	}
}

func register[I any, O any](r *Registry, wf Workflow[I, O], opts ...WorkflowOption) error {
	if wf == nil {
		return fmt.Errorf("workflow is nil")
	}
	if r == nil {
		return fmt.Errorf("registry is nil")
	}

	options := workflowOptions{
		codec:       JSONCodec{},
		concurrency: 1,
	}
	for _, opt := range opts {
		opt(&options)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	name := wf.Name()
	if name == "" {
		return fmt.Errorf("workflow name is empty")
	}
	if _, ok := r.workflows[name]; ok {
		return fmt.Errorf("workflow already registered: %s", name)
	}
	r.workflows[name] = registeredWorkflow[I, O]{
		wf:             wf,
		codecImpl:      options.codec,
		concurrencyVal: options.concurrency,
	}
	return nil
}

func (r *Registry) get(name string) (workflowRunner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wf, ok := r.workflows[name]
	return wf, ok
}

// list returns all registered workflow runners.
func (r *Registry) list() []workflowRunner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]workflowRunner, 0, len(r.workflows))
	for _, wf := range r.workflows {
		result = append(result, wf)
	}
	return result
}

// listCron returns all registered cron entries.
func (r *Registry) listCron() []cronEntry {
	r.cronMu.RLock()
	defer r.cronMu.RUnlock()
	out := make([]cronEntry, len(r.cronSchedules))
	copy(out, r.cronSchedules)
	return out
}

// RegisterCron registers a workflow with a cron schedule so the worker creates
// runs automatically.
//
// The workflow must also be registered via [Register] (or the same call can be combined).
// If it is not already registered, RegisterCron registers it with the given options.
//
// scheduleID uniquely identifies this schedule. Using the workflow name is a good default.
// If empty, it defaults to wf.Name().
//
// Go does not support type parameters on methods, so this is a package-level generic.
func RegisterCron[I any, O any](r *Registry, wf Workflow[I, O], input *I, scheduleID string, schedule Schedule, opts ...WorkflowOption) {
	if err := registerCron(r, wf, input, scheduleID, schedule, opts...); err != nil {
		panic(err)
	}
}

func registerCron[I any, O any](r *Registry, wf Workflow[I, O], input *I, scheduleID string, schedule Schedule, opts ...WorkflowOption) error {
	if wf == nil {
		return fmt.Errorf("workflow is nil")
	}
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	if schedule == nil {
		return fmt.Errorf("schedule is nil")
	}
	if scheduleID == "" {
		scheduleID = wf.Name()
	}

	// Ensure the workflow is registered (idempotent if already done).
	_ = register(r, wf, opts...)

	options := workflowOptions{
		codec:       JSONCodec{},
		concurrency: 1,
	}
	for _, opt := range opts {
		opt(&options)
	}

	inputJSON, err := options.codec.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal cron input: %w", err)
	}

	// Determine the cronExpr string for storage.
	cronExpr := fmt.Sprintf("%v", schedule)
	switch s := schedule.(type) {
	case *CronSchedule:
		cronExpr = fmt.Sprintf("%v", s)
	case everySchedule:
		cronExpr = "@every " + s.interval.String()
	}

	r.cronMu.Lock()
	defer r.cronMu.Unlock()
	// Check for duplicate schedule ID.
	for _, e := range r.cronSchedules {
		if e.scheduleID == scheduleID {
			return fmt.Errorf("cron schedule already registered: %s", scheduleID)
		}
	}
	r.cronSchedules = append(r.cronSchedules, cronEntry{
		scheduleID:   scheduleID,
		workflowName: wf.Name(),
		schedule:     schedule,
		cronExpr:     cronExpr,
		inputJSON:    inputJSON,
	})
	return nil
}
