# Flows - Simple Durable Workflow Orchestration for Go

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![PostgreSQL](https://img.shields.io/badge/postgresql-18+-blue.svg)](https://www.postgresql.org/)
[![Status](https://img.shields.io/badge/status-implementation-yellow.svg)](https://github.com/nvcnvn/flows)

> A lightweight, type-safe workflow orchestration library for Go, inspired by Temporal but designed to be "far more simple"

## 🎯 Key Features

- **Transaction Support**: Atomic workflow starts, signals, queries with application state using `WithTx()`
- **Type-Safe Generics**: Compile-time type checking for workflows and activities
- **Deterministic Replay**: Fault-tolerant execution with automatic state recovery
- **PostgreSQL Native**: Leverages PostgreSQL 18's UUIDv7 and JSONB features
- **Multi-Tenant Isolation**: Built-in tenant isolation at the database level
- **Single Package API**: Clean, discoverable API with one import
- **Simple**: Library-based, no separate cluster or infrastructure

## 🚀 Quick Start

### Installation

```bash
go get github.com/nvcnvn/flows
```
Apply our schema in [[migrations](migrations)]
- [migrations/001_init.sql](migrations/001_init.sql): base schema for any postgres
- [migrations/002_citus.sql](migrations/002_citus.sql): optional - if you're use citus for sharding

### Define Workflows and Activities

#### Activity
Activities represent non-deterministic operations that interact with external systems. These operations are inherently fallible due to transient failures such as network timeouts, service unavailability, or rate limiting.  

Flows provides automatic handling of retry logic, exponential backoff, scheduling, and idempotency guarantees for activity executions.

Activity functions must conform to the signature: `func(ctx context.Context, input *Input) (*Output, error)`.  
Register activities using `flows.NewActivity` with a configured retry policy.

**Dependency Injection**: Use factory functions with closures to inject dependencies (database pools, API clients, etc.):

```go
// Services holds shared dependencies
type Services struct {
    Pool        *pgxpool.Pool
    PaymentAPI  *PaymentClient
    ShippingAPI *ShippingClient
}

// NewActivities creates activities with injected dependencies via closures
func NewActivities(svc *Services) *Activities {
    return &Activities{
        ChargePayment: flows.NewActivity(
            "charge-payment",
            func(ctx context.Context, input *ChargePaymentInput) (*ChargePaymentOutput, error) {
                // Access injected dependencies through closure
                result, err := svc.PaymentAPI.Charge(ctx, input.CustomerID, input.Amount)
                if err != nil {
                    return nil, err
                }
                return &ChargePaymentOutput{Success: result.Success}, nil
            },
            flows.DefaultRetryPolicy,
        ),
        
        ShipOrder: flows.NewActivity(
            "ship-order",
            func(ctx context.Context, input *ShipOrderInput) (*ShipOrderOutput, error) {
                tracking, err := svc.ShippingAPI.CreateShipment(ctx, input)
                if err != nil {
                    return nil, err
                }
                return &ShipOrderOutput{TrackingNumber: tracking}, nil
            },
            flows.DefaultRetryPolicy,
        ),
    }
}

type Activities struct {
    ChargePayment *flows.Activity[ChargePaymentInput, ChargePaymentOutput]
    ShipOrder     *flows.Activity[ShipOrderInput, ShipOrderOutput]
}
```

See [DEPENDENCY_INJECTION.md](DEPENDENCY_INJECTION.md) for detailed patterns and testing strategies.

#### Workflow
Workflows provide an imperative programming model for orchestrating complex business processes. Unlike traditional message queue architectures requiring separate handlers and scheduling mechanisms, workflows are expressed as standard Go functions.

The framework ensures deterministic execution through replay. Each workflow step is executed exactly once per replay iteration, preventing duplicate side effects for activities during transient failures or system restarts.
```go
// NewWorkflows creates workflows with injected activities
func NewWorkflows(activities *Activities) *Workflows {
    return &Workflows{
        OrderWorkflow: flows.New(
            "order-workflow",
            1,
            func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
                input := ctx.Input()
                orderUUID, err := ctx.UUIDv7()
                if err != nil {
                    return nil, err
                }
                orderID := orderUUID.String()

                // Step 1: Charge payment
                payment, err := flows.ExecuteActivity(ctx, activities.ChargePayment, &ChargePaymentInput{
                    CustomerID: input.CustomerID,
                    Amount:     input.TotalPrice,
                })
                if err != nil {
                    return nil, fmt.Errorf("payment failed: %w", err)
                }

                if !payment.Success {
                    return nil, fmt.Errorf("payment was declined")
                }

                // Step 2: Wait for warehouse confirmation
                confirmation, err := flows.WaitForSignal[OrderInput, WarehouseConfirmation](ctx, "warehouse-ready")
                if err != nil {
                    return nil, fmt.Errorf("warehouse confirmation failed: %w", err)
                }

                if !confirmation.Available {
                    return nil, fmt.Errorf("items not available in warehouse")
                }

                // Step 3: Ship order
                shipment, err := flows.ExecuteActivity(ctx, activities.ShipOrder, &ShipOrderInput{
                    OrderID: orderID,
                    Items:   input.Items,
                })
                if err != nil {
                    return nil, fmt.Errorf("shipping failed: %w", err)
                }

                return &OrderOutput{
                    OrderID:        orderID,
                    TrackingNumber: shipment.TrackingNumber,
                    Status:         "shipped",
                }, nil
            },
        ),
    }
}

type Workflows struct {
    OrderWorkflow *flows.Workflow[OrderInput, OrderOutput]
}
```

The framework provides deterministic helper functions:
- **Sleep**: Suspends workflow execution until a specified time, enabling durable timer-based scheduling
- **Random, UUIDv7, Time**: Deterministic generators that produce consistent results during replay

**Critical**: Use these helpers exclusively to maintain workflow determinism. Workflows must behave as pure functions with reproducible execution paths, enabling safe replay after timer expiration or process failure recovery.

### Worker and Engine

**Worker**: A long-running process that polls and executes workflow tasks from the PostgreSQL queue.  
**Engine**: The client interface for starting workflows, sending signals, and querying execution state. This can be integrated into REST APIs, CLI tools, or any application component.

Workers and engines can coexist in the same process or run separately. Both require a PostgreSQL connection pool:

```go
// 1. Initialize dependencies
pool, err := pgxpool.New(ctx, connString)
if err != nil {
	log.Fatalf("Failed to connect to database: %v", err)
}
defer pool.Close()

services := &Services{
    Pool:        pool,
    PaymentAPI:  NewPaymentClient(),
    ShippingAPI: NewShippingClient(),
}

// 2. Create activities and workflows with dependencies
activities := NewActivities(services)
workflows := NewWorkflows(activities)

// 3. Start worker
worker := flows.NewWorker(pool, flows.WorkerConfig{
	Concurrency:   5,
	WorkflowNames: []string{"order-workflow"},
	PollInterval:  500 * time.Millisecond,
})
defer worker.Stop()
go worker.Run(ctx)
```

#### Engine Usage
```go
// 4. Use engine to start workflows
engine := flows.NewEngine(pool)

// Tenant isolation: Extract from authentication context or use fixed value for single-tenant deployments
tenantID := extractTenantID(authToken)
ctx = flows.WithTenantID(ctx, tenantID)

// Start workflow asynchronously
exec, err := flows.StartWith(engine, ctx, workflows.OrderWorkflow, &OrderInput{})
// REST API pattern: Return 201 Created with workflow_name and workflow_id for async processing
// Synchronous pattern: Block and wait for completion
result, err = flows.WaitForResultWith[OrderOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
```
See [USAGE.md](USAGE.md) for additional operations: Query, Signal, Cancel

Finally, please check [demo](demo) folder for real world example.

### Note about Workflow Name Sharding

- **Original Workflow Names Required**: APIs accept the base workflow name (e.g., `"loan-application"`), not the sharded name
- **Automatic Hash Suffix**: The library computes a shard suffix internally (e.g., `"loan-application-shard-0"`)
- **Workers Poll All Shards**: Workers automatically poll all sharded variants of their workflow type
- **Configurable**: Default 9 shards, configure via `flows.SetShardConfig()` before starting workflows

```go
// You use the original name - sharding happens internally
exec, err := flows.Start(ctx, MyWorkflow, input)  // -> "my-workflow-shard-[0,8]"
```

You can create partition or sharding based on workflow_name column.
