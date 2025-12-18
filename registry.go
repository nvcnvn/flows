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
