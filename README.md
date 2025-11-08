# Flows - Simple Durable Workflow Orchestration for Go

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![PostgreSQL](https://img.shields.io/badge/postgresql-18+-blue.svg)](https://www.postgresql.org/)
[![Status](https://img.shields.io/badge/status-implementation-yellow.svg)](https://github.com/nvcnvn/flows)

> A lightweight, type-safe workflow orchestration library for Go, inspired by Temporal but designed to be "far more simple"

## 🎯 Key Features

- **Type-Safe Generics**: Compile-time type checking for workflows and activities
- **Deterministic Replay**: Fault-tolerant execution with automatic state recovery
- **PostgreSQL Native**: Leverages PostgreSQL 18's UUIDv7 and JSONB features
- **Multi-Tenant Isolation**: Built-in tenant isolation at the database level
- **Transaction Support**: Atomic workflow starts with application state using `WithTx()`
- **Single Package API**: Clean, discoverable API with one import
- **Simple**: Library-based, no separate cluster or infrastructure

## 🚀 Quick Start

### Installation

```bash
go get github.com/nvcnvn/flows
```

**Requirements**: PostgreSQL 18+ (for UUIDv7 support)

### Basic Example

```go
import (
    "github.com/nvcnvn/flows"
    "github.com/jackc/pgx/v5/pgxpool"
)

// 1. Setup engine
pool, _ := pgxpool.New(ctx, "postgres://localhost/mydb")
engine := flows.NewEngine(pool)
flows.SetEngine(engine)

// 2. Define workflow
var OrderWorkflow = flows.New(
    "order-workflow", 1,
    func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
        // Execute activities
        payment, err := flows.ExecuteActivity(ctx, ChargePayment, &ChargeInput{...})
        if err != nil {
            return nil, err
        }
        
        // Wait for signals
        conf, err := flows.WaitForSignal[OrderInput, Confirmation](ctx, "ready")
        if err != nil {
            return nil, err
        }
        
        return &OrderOutput{Status: "completed"}, nil
    },
)

// 3. Start workflow
ctx = flows.WithTenantID(ctx, tenantID)
exec, err := flows.Start(ctx, OrderWorkflow, &OrderInput{...})
```

## 🏗️ Architecture

```
┌─────────────────────────────────┐
│     Your Application            │
│  flows.Start() → Execution      │
│  flows.WithTx(tx) → atomic ops  │
└─────────────────────────────────┘
              ↓
┌─────────────────────────────────┐
│       Flows Engine              │
│  • Workflow Registry            │
│  • Task Queue Management        │
│  • Multi-Tenant Context         │
└─────────────────────────────────┘
              ↓
┌─────────────────────────────────┐
│         Workers                 │
│  • Workflow Executor (replay)   │
│  • Activity Executor (retry)    │
│  • Timer & Signal Processors    │
└─────────────────────────────────┘
              ↓
┌─────────────────────────────────┐
│     PostgreSQL Storage          │
│  • UUIDv7 time-sortable IDs     │
│  • JSONB flexible storage       │
│  • ACID transactions            │
└─────────────────────────────────┘
```

## 📦 Project Structure

```
flows/
├── Public API (import "github.com/nvcnvn/flows")
├── flows.go          # Start, SendSignal, Query, RerunFromDLQ
├── workflow.go       # Workflow[In,Out], Context[T]
├── activity.go       # Activity[In,Out]
├── worker.go         # Worker, WorkerConfig
├── types.go          # Core types and errors
├── options.go        # WithTx, WithTenantID options
│
├── internal/         # Internal implementation
│   ├── storage/      # PostgreSQL operations
│   ├── registry/     # Type registration
│   └── executor/     # Workflow/activity execution
│
├── migrations/       # Database schema
└── examples/         # Working examples
```

## ✨ Highlights

### Transaction Atomicity

```go
tx, _ := db.Begin(ctx)
tx.Exec("INSERT INTO orders ...")
flows.Start(ctx, OrderWorkflow, input, flows.WithTx(tx))
tx.Commit() // Atomic!
```

### Type Safety

```go
var wf = flows.New(
    "my-workflow", 1,
    func(ctx *flows.Context[Input]) (*Output, error) {
        // Compile-time type checking!
    },
)
```

### Multi-Tenancy

```go
ctx = flows.WithTenantID(ctx, tenantID)
// All operations automatically isolated by tenant
```

## 📊 Status

🚧 **Implementation in Progress**

**Completed**:
- ✅ Database schema and migrations
- ✅ Storage layer (PostgreSQL)
- ✅ Type registry system
- ✅ Public API design
- ✅ Worker infrastructure
- ✅ Example applications

**In Progress**:
- 🔄 Workflow executor with complete replay
- 🔄 Activity retry logic
- 🔄 Timer and signal processing
- 🔄 Comprehensive tests

See [IMPLEMENTATION_SUMMARY.md](IMPLEMENTATION_SUMMARY.md) for detailed status.

## 🆚 Comparison with Temporal

| Feature | Temporal | Flows |
|---------|----------|-------|
| Language | Multi-language | Go only |
| Type Safety | Dynamic (any) | Generic (compile-time) |
| Deployment | Separate cluster | Library + PostgreSQL |
| Complexity | High | Low |
| Multi-tenancy | Enterprise feature | Built-in |
| Learning Curve | Steep | Gentle |

## 🤝 Contributing

Contributions welcome! Please:
1. Read the design document below
2. Check [DEVELOPMENT.md](DEVELOPMENT.md) for setup
3. Follow the "far more simple" philosophy
4. Maintain type safety
5. Add tests for new features

## 📄 License

MIT

---

# Flows - System Design Document

## Overview

Flows is a durable workflow execution system inspired by Temporal/Cadence but designed to be "far more simple". It provides safe-duration workflow function execution with deterministic replay, type-safe generics, and multi-tenant isolation.

## Core Principles

1. **Simplicity First**: Simpler than Temporal while maintaining core workflow orchestration capabilities
2. **Type Safety**: Generic APIs with compile-time type checking for workflows and activities
3. **Deterministic Execution**: Replay mechanism with cached results for fault tolerance
4. **Multi-Tenant Isolation**: First-class tenant isolation at the database level
5. **PostgreSQL Native**: Leverage PostgreSQL 18's UUIDv7 and JSONB features

## System Architecture

### High-Level Components

```
┌─────────────────────────────────────────────────────────────┐
│                      Client Application                      │
│  flows.Start[In,Out]() → Execution[Out]                     │
│  flows.SendSignal[P]() → signal workflow                    │
│  flows.Query() → get status                                 │
│  flows.RerunFromDLQ() → retry failed workflow               │
│  flows.WithTx(tx) → atomic operations with app state        │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│                      Flows Engine                            │
│  • Workflow Registration & Type Registry                    │
│  • Activity Registration                                     │
│  • Task Queue Management                                     │
│  • Multi-Tenant Context Propagation                          │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│                        Workers                               │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │   Workflow   │  │   Activity   │  │    Timer     │      │
│  │   Executor   │  │   Executor   │  │  Processor   │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│                   PostgreSQL 18 Storage                      │
│  • Workflows, Activities, History Events                    │
│  • Timers, Signals, Task Queue                              │
│  • Dead Letter Queue                                         │
│  • UUIDv7 time-sortable IDs                                 │
└─────────────────────────────────────────────────────────────┘
```

## Key Requirements

### 1. Deterministic Execution

**Problem**: Workflows must be resumable after failure or pause without re-executing completed activities.

**Solution**: 
- Workflow functions are **pure** - no direct I/O, network calls, or non-deterministic operations
- All side effects go through `WorkflowContext` primitives:
  - `ExecuteActivity[In, Out]()` - Execute external operations
  - `Sleep(duration)` - Deterministic delays
  - `Random()` - Deterministic random (seeded by workflow ID + sequence)
  - `UUIDv7()` - Deterministic UUID generation
  - `WaitForSignal[P]()` - Wait for external signals
- Activity results cached in `workflows.activity_results` JSONB column
- Replay mechanism: Re-run workflow function with cached results, skip completed activities

**Enforcement**:
- AST analyzer (go vet integration) checks workflow functions for:
  - No `time.Now()`, `rand.*`, `uuid.New()` calls
  - No network/file I/O
  - Only allowed primitives from `WorkflowContext`

### 2. Type-Safe Generic APIs

**Problem**: Prevent runtime type mismatches when starting workflows and consuming results.

**Solution**:
```go
// Define workflow with typed input/output
var MyWorkflow = workflow.New(
    "my-workflow", 1,
    func(ctx *workflow.Context[Input]) (*Output, error) {
        // Implementation
    },
)

// Type-safe execution - compile-time checked
exec := flows.Start(ctx, MyWorkflow, &Input{...})
result, err := exec.Get(ctx) // Returns *Output
```

**Type Registry**:
- Fully qualified type names: `"github.com/user/pkg.TypeName"`
- Auto-registration on workflow/activity creation
- Prevents collisions across packages
- `types.Register[T]()` → `TypeInfo{Name, Type}`
- `types.Deserialize(typeName, jsonBytes)` → instance

### 3. Transactional Atomicity with Application State

**Problem**: Workflows must start atomically with application database operations to prevent inconsistent state.

**Solution - Transaction Interface**:
```go
// Transaction interface for external transactions
// Compatible with pgx.Tx interface
type Tx interface {
    Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
}

// Option pattern for transaction support
type StartOption func(*startConfig)
func WithTx(tx Tx) StartOption

// Start workflow within user's transaction
func Start[In, Out any](
    ctx context.Context,
    wf *Workflow[In, Out],
    input *In,
    opts ...StartOption,
) (*Execution[Out], error)
```

**Use Case**:
```go
// User's application service
func CreateOrderWithWorkflow(ctx context.Context, orderReq *OrderRequest) error {
    tx, _ := db.Begin(ctx)
    defer tx.Rollback()
    
    // 1. Create order in app database
    order := &Order{ID: uuid.New(), ...}
    if err := tx.Insert(order); err != nil {
        return err // Rolls back, no workflow started
    }
    
    // 2. Start workflow atomically with same transaction
    _, err := flows.Start(ctx, OrderWorkflow, &OrderInput{
        OrderID: order.ID,
    }, flows.WithTx(tx))
    if err != nil {
        return err // Rolls back both app data and workflow
    }
    
    // 3. Commit everything together
    return tx.Commit() // Order + workflow start atomically
}
```

**Implementation Details**:
- If `WithTx()` provided: Use user's transaction, don't commit/rollback
- If no transaction: Create internal transaction, auto-commit on success
- Task queue insertion happens within same transaction
- Workflow not visible to workers until transaction commits

**Benefits**:
- Prevents orphaned workflows when app transaction fails
- Prevents inconsistent state when workflow starts but app data rolls back
- Natural integration with existing PostgreSQL-based applications
- Backward compatible via option pattern

**Applies To**:
- `flows.Start()` - Start workflow atomically with app state
- `flows.SendSignal()` - Send signal only if app transaction commits
- `flows.RerunFromDLQ()` - Rerun workflow atomically with app cleanup

### 4. Multi-Tenant Isolation

**Problem**: Single system serving multiple customers with data isolation.

**Solution**:
- Every table has `tenant_id UUID` column
- Context propagation: `workflow.WithTenantID(ctx, tenantID)`
- All storage queries filtered by `WHERE tenant_id = $1`
- Composite indexes: `(tenant_id, workflow_id)`, `(tenant_id, status)`, etc.
- Worker isolation: Tasks can be filtered by tenant (optional)

**Security**:
- Tenant ID extracted via `workflow.MustGetTenantID(ctx)` - panics if missing
- No cross-tenant data leakage possible
- Each tenant effectively has isolated tables

**Transaction Compatibility**:
- `tenant_id` extracted from context, not transaction
- Works seamlessly with `WithTx()` option
- All workflow queries within transaction filtered by tenant

### 5. Retry Policies & Dead Letter Queue

**Problem**: Activities fail due to transient errors or terminal failures.

**Solution - Retry Policy**:
```go
RetryPolicy {
    InitialInterval: 1s      // Start with 1s delay
    MaxInterval:     1h      // Cap at 1 hour
    BackoffFactor:   2.0     // Exponential: 1s, 2s, 4s, 8s...
    MaxAttempts:     10      // Give up after 10 tries
    Jitter:          0.1     // ±10% randomization
}
```

**Error Taxonomy**:
- `RetryableError` - Retry with backoff (e.g., network timeout)
- `TerminalError` - Don't retry, move to DLQ (e.g., invalid input)
- Default: All errors are retryable unless marked terminal

**Dead Letter Queue**:
- Failed workflows/activities moved to `dead_letter_queue` table
- Stores full context: input, error, metadata
- Manual retry: `flows.RerunFromDLQ(dlqID)` creates **new workflow**
- Unlimited manual retries (each as separate workflow)
- Metadata tracks DLQ lineage: `{dlq_id, original_workflow_id, attempt}`
- `RerunFromDLQ()` supports `WithTx()` for atomic retry with app cleanup

### 6. Workflow Primitives

All workflow operations go through `WorkflowContext[T]`:

#### ExecuteActivity
```go
result, err := ctx.ExecuteActivity(MyActivity, &ActivityInput{...})
// During replay: returns cached result
// During execution: schedules activity, pauses workflow, waits for completion
```

#### Sleep
```go
ctx.Sleep(5 * time.Minute)
// Creates timer in `timers` table with fire_at timestamp
// Pauses workflow until timer fires
```

#### Random (Deterministic)
```go
r := ctx.Random()
choice := r.Intn(100) // Deterministic based on workflow ID + sequence
// Always returns same value during replay
```

#### UUIDv7 (Deterministic)
```go
id := ctx.UUIDv7()
// Deterministic UUID based on workflow ID + sequence
```

#### WaitForSignal
```go
payload, err := ctx.WaitForSignal[SignalPayload]("approval")
// Pauses workflow until external signal arrives
// Type-safe: payload is *SignalPayload
```

### 7. Pause/Resume Mechanism

**Problem**: How to pause workflow execution mid-function without complex state machines?

**Solution - Panic/Recover**:
```go
// Workflow executor
func Execute(ctx context.Context, wf *Workflow) {
    defer func() {
        if r := recover(); r != nil {
            if errors.Is(r.(error), errors.ErrWorkflowPaused) {
                // Normal pause - persist state and exit
                return
            }
            panic(r) // Re-panic unexpected errors
        }
    }()
    
    result := wf.fn(workflowCtx) // Execute workflow function
    // Workflow completed
}
```

**How It Works**:
1. Workflow calls `ctx.ExecuteActivity()`
2. Activity not in cache → needs execution
3. `pauseFunc()` called → `panic(ErrWorkflowPaused)`
4. Workflow executor catches panic, persists state, exits
5. Activity executes asynchronously
6. Worker restarts workflow → activity result in cache → no panic
7. Workflow continues from same point

**Benefits**:
- No manual state machine code
- Normal Go control flow (if/for/switch)
- Transparent to workflow author

### 8. PostgreSQL 18 - UUIDv7

**Problem**: Need time-sortable IDs without separate `created_at` columns.

**Solution - UUIDv7**:
- PostgreSQL 18's `gen_random_uuid_v7()` generates RFC 9562 UUIDv7
- First 48 bits: Unix timestamp (milliseconds)
- Remaining bits: Random data
- Automatically sortable by time: `ORDER BY id` ≈ `ORDER BY created_at`

**Benefits**:
- Eliminate redundant `created_at`, `received_at`, `failed_at` columns
- Simpler schema
- Composite indexes use `id` instead of timestamp
- Query patterns: `ORDER BY id ASC/DESC` for FIFO/LIFO

**Usage**:
```sql
CREATE TABLE workflows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    -- No created_at needed!
);

-- Time-based queries
SELECT * FROM workflows 
WHERE tenant_id = $1 
ORDER BY id DESC  -- Newest first
LIMIT 10;
```

### 9. Worker Architecture

**Distributed Task Processing**:
```go
WorkerConfig {
    Concurrency:   10              // 10 concurrent task handlers
    WorkflowTypes: []string{"wf1"} // Handle specific workflow types
    PollInterval:  1 * time.Second // Poll task queue every 1s
}
```

**Task Queue - SELECT FOR UPDATE SKIP LOCKED**:
```sql
-- Atomically claim task for processing
SELECT * FROM task_queue
WHERE tenant_id = $1 
  AND workflow_name = ANY($2)  -- Worker type filtering
  AND visibility_timeout < NOW()
ORDER BY id ASC  -- FIFO based on UUIDv7
LIMIT 1
FOR UPDATE SKIP LOCKED;  -- Skip locked rows, no blocking

-- Update visibility timeout
UPDATE task_queue 
SET visibility_timeout = NOW() + interval '5 minutes'
WHERE id = $1;
```

**Worker Components**:
1. **Workflow Executor** - Execute workflow functions with replay
2. **Activity Executor** - Execute activities with retry logic
3. **Timer Processor** - Poll `timers` table, resume workflows when `fire_at <= NOW()`
4. **Signal Processor** - Process incoming signals, resume waiting workflows

**Graceful Shutdown**:
- Workers finish in-flight tasks before exit
- Tasks beyond visibility timeout auto-requeue
- No task loss during deployment

### 10. Database Schema

**Core Tables**:

```sql
-- Workflows: Main workflow state
CREATE TABLE workflows (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,
    version         INT NOT NULL,
    status          TEXT NOT NULL,  -- pending, running, completed, failed
    input           JSONB,
    output          JSONB,
    error           TEXT,
    sequence_num    INT DEFAULT 0,
    activity_results JSONB DEFAULT '{}'::jsonb,  -- Cache for replay
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_workflows_tenant_status ON workflows(tenant_id, status);

-- Activities: Activity execution state
CREATE TABLE activities (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id),
    name            TEXT NOT NULL,
    sequence_num    INT NOT NULL,
    status          TEXT NOT NULL,  -- scheduled, running, completed, failed
    input           JSONB,
    output          JSONB,
    error           TEXT,
    attempt         INT DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    backoff_ms      BIGINT,
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_activities_workflow ON activities(tenant_id, workflow_id, sequence_num);

-- History Events: Audit log
CREATE TABLE history_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL,
    sequence_num    INT NOT NULL,
    event_type      TEXT NOT NULL,  -- WORKFLOW_STARTED, ACTIVITY_SCHEDULED, etc.
    event_data      JSONB
);
CREATE INDEX idx_history_workflow ON history_events(tenant_id, workflow_id, sequence_num);

-- Timers: Sleep operations
CREATE TABLE timers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL,
    sequence_num    INT NOT NULL,
    fire_at         TIMESTAMPTZ NOT NULL,
    fired           BOOLEAN DEFAULT FALSE
);
CREATE INDEX idx_timers_fire ON timers(tenant_id, fire_at) WHERE NOT fired;

-- Signals: External events
CREATE TABLE signals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL,
    signal_name     TEXT NOT NULL,
    payload         JSONB,
    consumed        BOOLEAN DEFAULT FALSE
);
CREATE INDEX idx_signals_workflow ON signals(tenant_id, workflow_id, signal_name) WHERE NOT consumed;

-- Task Queue: Distributed work queue
CREATE TABLE task_queue (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id           UUID NOT NULL,
    workflow_id         UUID NOT NULL,
    workflow_name       TEXT NOT NULL,  -- For worker type filtering
    task_type           TEXT NOT NULL,  -- workflow, activity, timer
    visibility_timeout  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_task_queue_poll ON task_queue(tenant_id, workflow_name, visibility_timeout);

-- Dead Letter Queue: Failed tasks
CREATE TABLE dead_letter_queue (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id               UUID NOT NULL,
    workflow_id             UUID NOT NULL,
    workflow_name           TEXT NOT NULL,
    input                   JSONB,
    error                   TEXT,
    attempt                 INT,
    metadata                JSONB,  -- {dlq_id, original_workflow_id, attempt}
    rerun_as_workflow_id    UUID    -- Set when rerun from DLQ
);
CREATE INDEX idx_dlq_tenant ON dead_letter_queue(tenant_id, id DESC);
```

## API Design

### Package Structure Philosophy

**Single Import Pattern**: Developers import one package: `flows`

```go
import "github.com/nvcnvn/flows"

// Everything accessible through flows.*
// - flows.New() - Define workflow
// - flows.Context - Workflow context
// - flows.Start() - Start workflow
// - flows.SendSignal() - Send signal
// - flows.WithTenantID() - Set tenant
// - flows.WithTx() - Use transaction
```

### Starting Workflows

```go
import "github.com/nvcnvn/flows"

// Define workflow (typically at package level)
var OrderWorkflow = flows.New(
    "order-workflow",
    1,  // version
    func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
        // Validate order
        
        // Charge payment
        payment, err := ctx.ExecuteActivity(ChargePaymentActivity, &ChargeInput{...})
        if err != nil {
            return nil, err
        }
        
        // Wait for warehouse confirmation
        confirmation, err := ctx.WaitForSignal[WarehouseConfirmation]("warehouse-ready")
        if err != nil {
            return nil, err
        }
        
        // Ship order
        shipment, err := ctx.ExecuteActivity(ShipOrderActivity, &ShipInput{...})
        if err != nil {
            return nil, err
        }
        
        return &OrderOutput{
            OrderID:    confirmation.OrderID,
            Tracking:   shipment.TrackingNumber,
        }, nil
    },
)

// Start workflow (simple case - no transaction)
ctx = flows.WithTenantID(ctx, tenantID)
exec := flows.Start(ctx, OrderWorkflow, &OrderInput{
    CustomerID: "cust-123",
    Items:      []string{"item-1", "item-2"},
})

// Start workflow atomically with application transaction
tx, _ := db.Begin(ctx)
defer tx.Rollback()

// Insert app data
if err := tx.Exec("INSERT INTO orders ..."); err != nil {
    return err // Rolls back, workflow not started
}

// Start workflow in same transaction
exec := flows.Start(ctx, OrderWorkflow, &OrderInput{...}, flows.WithTx(tx))
if err := tx.Commit(); err != nil {
    return err // Both app data and workflow rolled back
}

// Get result (blocks until completion)
result, err := exec.Get(ctx)  // Returns *OrderOutput
```

### Sending Signals

```go
import "github.com/nvcnvn/flows"

// Send signal to workflow (simple case)
err := flows.SendSignal(ctx, workflowID, "warehouse-ready", &WarehouseConfirmation{
    OrderID:   "ord-456",
    Available: true,
})

// Send signal atomically with app state update
tx, _ := db.Begin(ctx)
defer tx.Rollback()

// Update inventory
if err := tx.Exec("UPDATE inventory SET reserved = true WHERE id = $1", itemID); err != nil {
    return err
}

// Send signal only if inventory update succeeds
err := flows.SendSignal(ctx, workflowID, "warehouse-ready", 
    &WarehouseConfirmation{OrderID: "ord-456", Available: true},
    flows.WithTx(tx))
if err != nil {
    return err
}

return tx.Commit() // Both inventory update and signal sent atomically
```

### Querying Workflow Status

```go
status, err := flows.Query(ctx, workflowID)
// Returns: {Status: "running", Output: nil, Error: nil}
```

### Rerunning from DLQ

```go
// List failed workflows
dlqRecords, err := store.ListDLQ(ctx, tenantID, 10)

// Rerun failed workflow as new instance (simple case)
newWorkflowID, err := flows.RerunFromDLQ(ctx, dlqRecords[0].ID)

// Rerun with cleanup in same transaction
tx, _ := db.Begin(ctx)
defer tx.Rollback()

// Clean up related app state
if err := tx.Exec("DELETE FROM failed_orders WHERE workflow_id = $1", dlqRecords[0].WorkflowID); err != nil {
    return err
}

// Rerun workflow atomically
newWorkflowID, err := flows.RerunFromDLQ(ctx, dlqRecords[0].ID, flows.WithTx(tx))
if err != nil {
    return err
}

return tx.Commit() // Cleanup + rerun atomically
```

## Code Organization

**Philosophy**: Flat internal structure, clean single-package public API

```
flows/
├── DESIGN.md              # This document
├── README.md              # User-facing documentation
├── go.mod
├── migrations/
│   └── 001_init.sql       # Database schema
│
├── Public API (flows package root - import "github.com/nvcnvn/flows")
├── flows.go               # Public API: Start, SendSignal, Query, etc.
├── workflow.go            # Public API: New, Context[T], Execution[T]
├── activity.go            # Public API: Activity definition
├── worker.go              # Public API: Worker, WorkerConfig
├── options.go             # Public API: WithTx, WithTenantID, StartOption
├── types.go               # Public API: Tx interface, error types
│
├── Internal Implementation (internal/ - not importable by users)
├── internal/
│   ├── storage/
│   │   ├── postgres.go        # Database connection
│   │   ├── workflow_store.go  # Workflow CRUD
│   │   ├── activity_store.go  # Activity CRUD
│   │   ├── history_store.go   # History events
│   │   ├── timer_store.go     # Timer operations
│   │   ├── signal_store.go    # Signal operations
│   │   ├── queue.go           # Task queue operations
│   │   └── dlq_store.go       # Dead letter queue
│   ├── executor/
│   │   ├── workflow.go        # Workflow execution with replay
│   │   └── activity.go        # Activity execution with retry
│   ├── registry/
│   │   └── types.go           # Generic type registration
│   └── determinism/
│       └── analyzer.go        # AST analyzer for determinism
│
├── cmd/
│   └── determinism-checker/
│       └── main.go        # go vet integration
└── examples/
    └── order/
        └── main.go        # Example workflow
```

**Key Design Decisions**:

1. **Single Import**: Users only import `"github.com/nvcnvn/flows"`
2. **Flat Public API**: All public types/functions at package root
3. **Internal Package**: Implementation details hidden in `internal/`
4. **No Sub-package Imports**: Users never import `flows/workflow` or `flows/activity`
5. **Clean Namespace**: `flows.New()`, `flows.Context`, `flows.Start()`, etc.

**Developer Experience**:

```go
// Single import - everything needed
import "github.com/nvcnvn/flows"

// Define workflow
var wf = flows.New("my-workflow", 1, func(ctx *flows.Context[Input]) (*Output, error) {
    result, err := ctx.ExecuteActivity(myActivity, &ActivityInput{})
    return &Output{}, nil
})

// Start workflow
ctx = flows.WithTenantID(ctx, tenantID)
exec := flows.Start(ctx, wf, &Input{})

// Create worker
worker := flows.NewWorker(db, flows.WorkerConfig{
    Concurrency: 10,
    Workflows:   []flows.Workflow{wf},
})
worker.Run(ctx)
```

## Non-Functional Requirements

### Performance
- **Throughput**: 1000+ workflow starts/second per worker
- **Latency**: <10ms for workflow start, <100ms for signal delivery
- **Scalability**: Horizontal scaling via worker instances

### Reliability
- **Durability**: All state persisted to PostgreSQL before ACK
- **Fault Tolerance**: Workers can crash, workflows auto-resume
- **Data Loss**: Zero data loss (PostgreSQL ACID guarantees)

### Observability
- **History Events**: Complete audit log of all workflow actions
- **Metrics**: Task queue depth, workflow completion rate, DLQ size
- **Tracing**: Context propagation for distributed tracing integration

## Future Enhancements (Out of Scope)

- [ ] Workflow versioning & migration support
- [ ] Cron/scheduled workflows
- [ ] Workflow search by custom attributes
- [ ] Activity heartbeats for long-running tasks
- [ ] Workflow cancellation
- [ ] Child workflows
- [ ] Saga pattern helpers
- [ ] WebUI for workflow monitoring
- [ ] Metrics dashboard

## Comparison with Temporal

| Feature | Temporal | Flows |
|---------|----------|-------|
| Language | Go, Java, TypeScript, Python | Go only |
| Type Safety | Dynamic (any) | Generic (compile-time) |
| Deployment | Separate cluster | Library + PostgreSQL |
| Complexity | High (distributed system) | Low (library) |
| Multi-tenancy | Enterprise feature | Built-in |
| Learning Curve | Steep | Gentle |
| Versioning | Built-in | Manual (version field) |
| Visibility | Advanced UI | SQL queries |

**Flows Philosophy**: "Far more simple" - eliminate complexity where possible while preserving core workflow orchestration value.

## Decision Log

### Why Panic/Recover for Workflow Pausing?
**Alternatives Considered**:
1. Manual state machine - Too verbose, error-prone
2. Code generation - Complex build process
3. Continuation-passing style - Unnatural Go code

**Decision**: Panic/recover provides transparent suspension without manual state management. Trade-off: unconventional but ergonomic.

### Why UUIDv7 Instead of ULID or Snowflake?
**Alternatives Considered**:
1. ULID - Not native to PostgreSQL, requires extension
2. Snowflake - Requires centralized ID generator
3. UUID v4 + created_at - Redundant columns

**Decision**: UUIDv7 is PostgreSQL 18 native, time-sortable, no additional infrastructure needed.

### Why JSONB for Activity Results Cache?
**Alternatives Considered**:
1. Separate cache table - More normalized but slower
2. In-memory cache - Lost on worker restart
3. Binary encoding - Less debuggable

**Decision**: JSONB allows flexible storage, fast access, and is queryable/debuggable.

### Why Transaction Interface Instead of Always Creating Internal Transaction?
**Alternatives Considered**:
1. Always create internal transaction - Doesn't allow atomicity with app state
2. Two-phase commit - Too complex, not PostgreSQL native
3. Saga pattern only - Forces async compensation, not always desired

**Decision**: Option pattern with `WithTx()` provides:
- Backward compatibility (no breaking changes)
- Atomicity with application state when needed
- Simple internal transaction when not needed
- Natural PostgreSQL integration
- Prevents common consistency bugs (orphaned workflows, inconsistent state)

### Why Single Package API Instead of Separate flows/workflow/activity Packages?
**Alternatives Considered**:
1. Separate packages (flows, workflow, activity) - More "organized" but requires multiple imports
2. Sub-packages (flows/core, flows/worker) - Still multiple imports
3. Everything exported from flows/ - Namespace pollution

**Decision**: Single package with internal/ for implementation:
- **Developer Experience**: One import statement (`import "github.com/nvcnvn/flows"`)
- **Discoverability**: All public API in one place, easy to explore
- **Simplicity**: No confusion about which package to import
- **Clean Namespace**: `flows.New()`, `flows.Context`, `flows.Start()` - intuitive
- **Encapsulation**: Implementation details hidden in `internal/`
- **Common Pattern**: Similar to `context`, `errors`, `time` packages

Example comparison:
```go
// ❌ Multi-package (confusing)
import (
    "github.com/nvcnvn/flows"
    "github.com/nvcnvn/flows/workflow"
    "github.com/nvcnvn/flows/activity"
)
var wf = workflow.New(...)
flows.Start(ctx, wf, input)

// ✅ Single package (clean)
import "github.com/nvcnvn/flows"
var wf = flows.New(...)
flows.Start(ctx, wf, input)
```

### Why One Generic Type Registry?
**Alternatives Considered**:
1. Separate registries per package - Fragmented
2. No registry, just interfaces - Loses type safety
3. Reflection everywhere - Runtime errors

**Decision**: Single global registry with fully qualified names prevents collisions and enables type-safe deserialization.

## Security Considerations

1. **SQL Injection**: Use parameterized queries (pgx handles this)
2. **Tenant Isolation**: All queries filtered by tenant_id, no cross-tenant access
3. **Input Validation**: Workflow/activity authors responsible for input validation
4. **Secrets Management**: Pass secrets via context, not workflow input (visible in DB)
5. **Access Control**: Authentication/authorization outside Flows scope (caller responsibility)
6. **Transaction Safety**: When using `WithTx()`, ensure proper rollback/commit handling in application code

## Testing Strategy

1. **Unit Tests**: Each package independently tested
2. **Integration Tests**: Full workflow execution against test PostgreSQL
3. **Determinism Tests**: Verify replay produces identical results
4. **Chaos Tests**: Kill workers mid-execution, verify resumption
5. **Performance Tests**: Measure throughput and latency under load

## Glossary

- **Workflow**: Long-running business process composed of activities
- **Activity**: Single unit of work (typically I/O operation)
- **Replay**: Re-executing workflow function with cached activity results
- **Deterministic**: Always produces same output for same input
- **Signal**: External message sent to running workflow
- **Timer**: Scheduled delay in workflow execution
- **Sequence Number**: Monotonic counter for workflow operations
- **Visibility Timeout**: Time before task returns to queue if not completed
- **Dead Letter Queue**: Storage for permanently failed tasks
- **UUIDv7**: Time-sortable UUID with embedded timestamp (RFC 9562)

---

**Document Version**: 1.0  
**Last Updated**: 2025-11-08  
**Status**: Implementation in Progress
