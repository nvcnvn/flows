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

## Custom Hash Configuration

The library uses hashing to distribute workflows across shards for horizontal scaling. You can customize the hash configuration.

### Global Hash (Pattern 1)

```go
package main

import (
    "github.com/nvcnvn/flows"
)

func main() {
    // Option 1: Use default (9 shards)
    // No configuration needed - automatically created on first use
    
    // Option 2: Set custom shard config globally
    shardConfig := flows.NewShardConfig(16) // Use 16 shards
    flows.SetShardConfig(shardConfig)
    
    // Now all workflows use this shard config
    exec, err := flows.Start(ctx, myWorkflow, input)
}
```

### Explicit Shard Config (Pattern 2)

```go
package main

import (
    "github.com/nvcnvn/flows"
)

func main() {
    pool, _ := pgxpool.New(ctx, connString)
    
    // Option 1: Engine with default shard config (9 shards)
    engine := flows.NewEngine(pool)
    
    // Option 2: Engine with custom shard config
    sharder := flows.NewShardConfig(16) // Use 16 shards
    engine := flows.NewEngine(pool, flows.WithSharder(sharder))
    
    // Workers can use the same custom sharder
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        WorkflowNames: []string{"my-workflow"},
        Concurrency:   10,
        TenantID:      tenantID,
        Sharder:       sharder, // Use same sharder as engine
    })
}
```

**Shard Config Best Practices:**
- Use the same shard configuration across all engines and workers
- The number of shards is fixed at configuration time (no rebalancing)
- Choose a shard count based on your expected scale (9 is default, good for most cases)

### Custom Sharder Implementation

You can implement your own sharding strategy by implementing the `Sharder` interface:

```go
package main

import (
    "github.com/google/uuid"
    "github.com/nvcnvn/flows"
)

// CustomSharder implements custom sharding logic
type CustomSharder struct {
    numShards int
}

func (s *CustomSharder) GetShard(workflowID uuid.UUID) int {
    // Your custom sharding logic here
    // Example: use first byte of UUID
    return int(workflowID[0]) % s.numShards
}

func (s *CustomSharder) NumShards() int {
    return s.numShards
}

func main() {
    pool, _ := pgxpool.New(ctx, connString)
    
    // Use custom sharder
    sharder := &CustomSharder{numShards: 12}
    engine := flows.NewEngine(pool, flows.WithSharder(sharder))
    
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        WorkflowNames: []string{"my-workflow"},
        Sharder:       sharder, // Same sharder instance
    })
}
```

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
