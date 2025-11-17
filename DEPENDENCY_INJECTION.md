# Dependency Injection Patterns for Flows

## Problem Statement

The current API for defining workflows and activities doesn't provide a clear way to inject dependencies like:
- Database connection pools
- API clients (HTTP, gRPC)
- Cache connections (Redis, Memcached)
- Logger instances
- Configuration objects
- Business logic services

Users might resort to global variables, which makes testing difficult and limits flexibility.

## Recommended Solution: Closure Pattern (Factory Functions)

Since Go supports closures, the most idiomatic approach is to define **factory functions** that capture dependencies and return activity/workflow definitions.

### Pattern 1: Activity Factories

```go
package myapp

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nvcnvn/flows"
)

// Services holds all shared dependencies
type Services struct {
    Pool         *pgxpool.Pool
    PaymentAPI   *PaymentClient
    ShippingAPI  *ShippingClient
    Logger       *slog.Logger
}

// NewActivities creates all activities with injected dependencies
func NewActivities(svc *Services) *Activities {
    return &Activities{
        ChargePayment: flows.NewActivity(
            "charge-payment",
            func(ctx context.Context, input *ChargePaymentInput) (*ChargePaymentOutput, error) {
                // Access dependencies from closure
                svc.Logger.Info("charging payment", "customer", input.CustomerID)
                
                // Use database pool
                var balance float64
                err := svc.Pool.QueryRow(ctx, 
                    "SELECT balance FROM accounts WHERE customer_id = $1", 
                    input.CustomerID).Scan(&balance)
                if err != nil {
                    return nil, err
                }
                
                // Call external API
                result, err := svc.PaymentAPI.Charge(ctx, input.CustomerID, input.Amount)
                if err != nil {
                    return nil, err
                }
                
                return &ChargePaymentOutput{
                    Success:       result.Success,
                    TransactionID: result.TxID,
                }, nil
            },
            flows.DefaultRetryPolicy,
        ),
        
        ShipOrder: flows.NewActivity(
            "ship-order",
            func(ctx context.Context, input *ShipOrderInput) (*ShipOrderOutput, error) {
                svc.Logger.Info("shipping order", "order_id", input.OrderID)
                
                // Record shipping in database
                _, err := svc.Pool.Exec(ctx,
                    "INSERT INTO shipments (order_id, status) VALUES ($1, $2)",
                    input.OrderID, "processing")
                if err != nil {
                    return nil, err
                }
                
                // Call shipping API
                tracking, err := svc.ShippingAPI.CreateShipment(ctx, input)
                if err != nil {
                    return nil, err
                }
                
                return &ShipOrderOutput{
                    TrackingNumber: tracking,
                }, nil
            },
            flows.DefaultRetryPolicy,
        ),
    }
}

// Activities holds all activity definitions
type Activities struct {
    ChargePayment *flows.Activity[ChargePaymentInput, ChargePaymentOutput]
    ShipOrder     *flows.Activity[ShipOrderInput, ShipOrderOutput]
}
```

### Pattern 2: Workflow Factories

Workflows can also capture dependencies through closures:

```go
// NewWorkflows creates workflows with injected dependencies
func NewWorkflows(activities *Activities, svc *Services) *Workflows {
    return &Workflows{
        OrderWorkflow: flows.New(
            "order-workflow",
            1,
            func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
                input := ctx.Input()
                
                // You can access services if needed for deterministic operations
                // (but avoid non-deterministic calls like time.Now() or random.Rand())
                svc.Logger.Info("starting order workflow", "customer", input.CustomerID)
                
                // Execute activities (already have dependencies via closure)
                payment, err := flows.ExecuteActivity(ctx, activities.ChargePayment, &ChargePaymentInput{
                    CustomerID: input.CustomerID,
                    Amount:     input.TotalPrice,
                })
                if err != nil {
                    return nil, err
                }
                
                shipment, err := flows.ExecuteActivity(ctx, activities.ShipOrder, &ShipOrderInput{
                    OrderID: input.OrderID,
                    Items:   input.Items,
                })
                if err != nil {
                    return nil, err
                }
                
                return &OrderOutput{
                    OrderID:        input.OrderID,
                    TrackingNumber: shipment.TrackingNumber,
                }, nil
            },
        ),
    }
}

type Workflows struct {
    OrderWorkflow *flows.Workflow[OrderInput, OrderOutput]
}
```

### Pattern 3: Application Setup

```go
package main

import (
    "context"
    "log"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nvcnvn/flows"
    "myapp"
)

func main() {
    ctx := context.Background()
    
    // 1. Initialize dependencies
    pool, err := pgxpool.New(ctx, "postgres://localhost/mydb")
    if err != nil {
        log.Fatal(err)
    }
    defer pool.Close()
    
    services := &myapp.Services{
        Pool:        pool,
        PaymentAPI:  myapp.NewPaymentClient(),
        ShippingAPI: myapp.NewShippingClient(),
        Logger:      slog.Default(),
    }
    
    // 2. Create activities and workflows with dependencies
    activities := myapp.NewActivities(services)
    workflows := myapp.NewWorkflows(activities, services)
    
    // 3. Start worker
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        Concurrency:   5,
        WorkflowNames: []string{"order-workflow"},
        PollInterval:  500 * time.Millisecond,
    })
    
    go worker.Run(ctx)
    defer worker.Stop()
    
    // 4. Start workflow
    engine := flows.NewEngine(pool)
    exec, err := flows.StartWith(engine, ctx, workflows.OrderWorkflow, &myapp.OrderInput{
        CustomerID: "customer-123",
        OrderID:    "order-456",
        TotalPrice: 99.99,
    })
    if err != nil {
        log.Fatal(err)
    }
    
    log.Printf("Workflow started: %s", exec.WorkflowID())
}
```

## Alternative Pattern: Struct-Based Activities (More Boilerplate)

For teams that prefer explicit structs over closures:

```go
// ActivityRegistry holds activities with dependencies
type ActivityRegistry struct {
    services *Services
}

func NewActivityRegistry(svc *Services) *ActivityRegistry {
    ar := &ActivityRegistry{services: svc}
    
    // Register all activities
    ar.ChargePayment = flows.NewActivity(
        "charge-payment",
        ar.chargePayment,  // Method on struct
        flows.DefaultRetryPolicy,
    )
    
    return ar
}

// chargePayment is a method that has access to ar.services
func (ar *ActivityRegistry) chargePayment(
    ctx context.Context, 
    input *ChargePaymentInput,
) (*ChargePaymentOutput, error) {
    // Access dependencies via ar.services
    result, err := ar.services.PaymentAPI.Charge(ctx, input.CustomerID, input.Amount)
    return &ChargePaymentOutput{Success: result.Success}, nil
}

type ActivityRegistry struct {
    services      *Services
    ChargePayment *flows.Activity[ChargePaymentInput, ChargePaymentOutput]
    ShipOrder     *flows.Activity[ShipOrderInput, ShipOrderOutput]
}
```

## Key Advantages of These Patterns

1. **No Global State**: All dependencies are explicitly passed
2. **Testable**: Easy to inject mocks/stubs for testing
3. **Type-Safe**: Compiler ensures correct dependencies
4. **Flexible**: Different workers can use different implementations
5. **Standard Go**: Uses closures, a fundamental Go feature
6. **No Framework Magic**: Simple, explicit, readable

## Testing Example

```go
func TestOrderWorkflow(t *testing.T) {
    // Create test dependencies
    testPool := setupTestDB(t)
    mockPaymentAPI := &MockPaymentClient{
        ChargeFunc: func(ctx context.Context, customerID string, amount float64) (*PaymentResult, error) {
            return &PaymentResult{Success: true, TxID: "test-tx"}, nil
        },
    }
    
    testServices := &Services{
        Pool:       testPool,
        PaymentAPI: mockPaymentAPI,
        Logger:     slog.Default(),
    }
    
    // Create activities and workflows with test dependencies
    activities := NewActivities(testServices)
    workflows := NewWorkflows(activities, testServices)
    
    // Test workflow
    engine := flows.NewEngine(testPool)
    exec, err := flows.StartWith(engine, ctx, workflows.OrderWorkflow, input)
    require.NoError(t, err)
    
    // Verify mock was called
    assert.True(t, mockPaymentAPI.ChargeCalled)
}
```

## What NOT To Do

❌ **Don't use global variables:**
```go
var globalPool *pgxpool.Pool  // Bad: hard to test, not flexible

var ChargePaymentActivity = flows.NewActivity(
    "charge-payment",
    func(ctx context.Context, input *Input) (*Output, error) {
        globalPool.Query(...)  // Bad: relies on global state
    },
    flows.DefaultRetryPolicy,
)
```

❌ **Don't put dependencies in context.Context:**
```go
// Bad: context is for cancellation/deadlines, not dependencies
ctx = context.WithValue(ctx, "pool", pool)
```

## Summary

The **closure/factory pattern** is the recommended approach:
- Define factory functions that accept dependencies
- Return structs containing activity/workflow definitions
- Activities capture dependencies through closures
- Clean, testable, idiomatic Go code

This pattern works perfectly with the existing `flows.NewActivity` and `flows.New` APIs without any changes to the library.
