# Usage Patterns

The Flows library supports two usage patterns for maximum flexibility:

## Pattern 1: Global Engine (Recommended for Simple Applications)

Set up a global engine once and use package-level functions throughout your application.

```go
package main

import (
    "context"
    "log"
    
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nvcnvn/flows"
)

func main() {
    // Setup: Initialize engine once at application startup
    pool, err := pgxpool.New(context.Background(), "postgres://localhost/mydb")
    if err != nil {
        log.Fatal(err)
    }
    
    engine := flows.NewEngine(pool)
    flows.SetEngine(engine) // Set global engine
    
    // Usage: Use package-level functions anywhere in your app
    ctx := flows.WithTenantID(context.Background(), tenantID)
    
    // Start a workflow
    exec, err := flows.Start(ctx, myWorkflow, input)
    if err != nil {
        log.Fatal(err)
    }
    
    // Send a signal
    err = flows.SendSignal(ctx, exec.WorkflowName(), exec.WorkflowID(), "approval", payload)
    
    // Query workflow status
    info, err := flows.Query(ctx, exec.WorkflowName(), exec.WorkflowID())
    
    // Get result
    result, err := flows.GetResult[MyOutput](ctx, exec.WorkflowName(), exec.WorkflowID())
    
    // Wait for result (blocks until complete)
    result, err = flows.WaitForResult[MyOutput](ctx, exec.WorkflowName(), exec.WorkflowID())
}
```

**Advantages:**
- Clean, simple API
- Less boilerplate
- Good for applications with a single database connection
- Thread-safe global state

## Pattern 2: Explicit Engine Instance (Recommended for Multi-Tenant or Complex Applications)

Pass the engine explicitly for better control and testability.

```go
package main

import (
    "context"
    "log"
    
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nvcnvn/flows"
)

func main() {
    // Setup: Create engine instance
    pool, err := pgxpool.New(context.Background(), "postgres://localhost/mydb")
    if err != nil {
        log.Fatal(err)
    }
    
    engine := flows.NewEngine(pool)
    
    // Usage: Pass engine to functions
    ctx := flows.WithTenantID(context.Background(), tenantID)
    
    // Start a workflow
    exec, err := flows.StartWith(engine, ctx, myWorkflow, input)
    if err != nil {
        log.Fatal(err)
    }
    
    // Send a signal
    err = flows.SendSignalWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID(), "approval", payload)
    
    // Query workflow status
    info, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
    
    // Get result
    result, err := flows.GetResultWith[MyOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
    
    // Wait for result (blocks until complete)
    result, err = flows.WaitForResultWith[MyOutput](engine, ctx, exec.WorkflowName(), exec.WorkflowID())
}
```

**Advantages:**
- Explicit dependencies (better for dependency injection)
- Easier to test (mock the engine)
- Multiple engines per application
- Better for multi-database or sharded architectures
- No global state

## Custom Hash Ring Configuration

The library uses consistent hashing to distribute workflows across shards for horizontal scaling. You can customize the hash ring configuration.

### Global Hash Ring (Pattern 1)

```go
package main

import (
    "github.com/nvcnvn/flows"
)

func main() {
    // Option 1: Use default (3 shards)
    // No configuration needed - automatically created on first use
    
    // Option 2: Set custom hash ring globally
    hashRing := flows.NewConsistentHash(100) // 100 virtual nodes per shard
    for i := 0; i < 5; i++ {
        hashRing.Add(i) // Add 5 shards
    }
    flows.SetHashRing(hashRing)
    
    // Now all workflows use this hash ring
    exec, err := flows.Start(ctx, myWorkflow, input)
}
```

### Explicit Hash Ring (Pattern 2)

```go
package main

import (
    "github.com/nvcnvn/flows"
)

func main() {
    pool, _ := pgxpool.New(ctx, connString)
    
    // Option 1: Engine with default hash ring
    engine := flows.NewEngine(pool)
    
    // Option 2: Engine with custom hash ring
    hashRing := flows.NewConsistentHash(100)
    for i := 0; i < 5; i++ {
        hashRing.Add(i)
    }
    engine := flows.NewEngine(pool, flows.WithHashRing(hashRing))
    
    // Workers can use the same custom hash ring
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        WorkflowNames: []string{"my-workflow"},
        Concurrency:   10,
        TenantID:      tenantID,
        HashRing:      hashRing, // Use same hash ring as engine
    })
}
```

**Hash Ring Best Practices:**
- Use the same hash ring configuration across all engines and workers
- Higher replicas (e.g., 100) provide better distribution but use more memory
- Adding/removing shards with consistent hashing minimizes workflow reassignment
- Default is 3 shards with 100 virtual nodes per shard

## Pattern 3: Hybrid Approach

You can mix both patterns. For example, use global functions in HTTP handlers but pass engine explicitly in tests:

```go
// production code
func CreateOrderHandler(w http.ResponseWriter, r *http.Request) {
    ctx := flows.WithTenantID(r.Context(), getTenantID(r))
    exec, err := flows.Start(ctx, OrderWorkflow, input)
    // ...
}

// test code
func TestCreateOrder(t *testing.T) {
    engine := flows.NewEngine(testPool)
    ctx := flows.WithTenantID(context.Background(), testTenantID)
    exec, err := flows.StartWith(engine, ctx, OrderWorkflow, input)
    // ...
}
```

## API Correspondence

| Global Function | Explicit Function | Description |
|----------------|-------------------|-------------|
| `flows.Start()` | `flows.StartWith(engine, ...)` | Start a workflow |
| `flows.SendSignal()` | `flows.SendSignalWith(engine, ...)` | Send a signal |
| `flows.Query()` | `flows.QueryWith(engine, ...)` | Query workflow status |
| `flows.GetResult()` | `flows.GetResultWith(engine, ...)` | Get workflow result |
| `flows.WaitForResult()` | `flows.WaitForResultWith(engine, ...)` | Wait for result |
| `flows.RerunFromDLQ()` | `flows.RerunFromDLQWith(engine, ...)` | Rerun from DLQ |

## Choosing a Pattern

**Use Global Engine when:**
- Building a simple application
- Single database connection
- Want minimal boilerplate
- Comfortable with package-level state

**Use Explicit Engine when:**
- Building a library
- Need multiple database connections
- Want explicit dependency injection
- Writing extensive tests with mocks
- Building multi-tenant systems with database-per-tenant

Both patterns are fully supported and use the same underlying implementation, so you can choose based on your preferences and requirements.
