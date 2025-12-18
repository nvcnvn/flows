
# flows

Minimal, Postgres-backed durable workflow runner (Cadence/Temporal-style replay) in Go.

## Guarantees

- **Type-safe**: steps and events use Go generics; no user casting.
- **ACID-friendly**: the worker executes workflow code inside a `pgx.Tx`, so you can couple step outputs + business writes atomically.
- **Replay-based durability**: on resume, the worker re-runs the workflow function; completed steps/events/timers are memoized in Postgres and returned without re-executing.

## Important note about Go generics

Go currently does **not** support type parameters on methods, so the type-safe APIs are **package-level generic functions** like `flows.Execute(...)` and `flows.WaitForEvent[T](...)`.

## Schema

Create the required tables:

- Use the SQL in `flows.SchemaSQL` (see [schema.go](schema.go)).
- By default, Flows uses the `flows` schema with unprefixed table names (`runs`, `steps`, ...).
- To install into a different schema, use `flows.SchemaSQLFor("my_schema")`.

To point the worker/client at a custom schema:

```go
cfg := flows.DBConfig{Schema: "my_schema"}

client := flows.Client{DBConfig: cfg}
worker := flows.Worker{Pool: pool, Registry: reg, DBConfig: cfg}
```

## Sharding (Citus)

Flows supports sharded Postgres setups (e.g. Citus) by routing all reads/writes using a
distribution column `workflow_name_shard`.

- Configure shard fan-out via `flows.DBConfig{ShardCount: N}`.
- Each run is assigned to a shard deterministically as `workflow_name_<k>` where $0 \le k < N$.
- The primary key for a run is `(workflow_name_shard, run_id)`; all child tables include the same shard key.

Because of this, run identifiers are represented as `flows.RunKey`:

```go
type RunKey struct {
	WorkflowNameShard string
	RunID             flows.RunID
}
```

### Worker concurrency

Each workflow type runs in its own goroutine pool with configurable concurrency.
This ensures busy workflows don't block other workflow types.

```go
reg := flows.NewRegistry()
flows.Register(reg, myFastWorkflow)  // default concurrency: 1
flows.Register(reg, mySlowWorkflow, flows.WithConcurrency(10))  // 10 goroutines

worker := flows.Worker{Pool: pool, Registry: reg, DBConfig: flows.DBConfig{ShardCount: 32}}
worker.Run(ctx)  // starts goroutine pools for each workflow type
```

### Citus Compatibility

On Citus, `SELECT ... FOR UPDATE SKIP LOCKED` must be routed to a single shard. The worker
achieves this by including `workflow_name_shard` (the distribution column) in the WHERE clause.

The worker iterates through all shards for each workflow, using round-robin rotation to
prevent starvation when some shards have more work than others. This happens automatically
based on `DBConfig.ShardCount`.

## Example

See [examples/simple/example.go](examples/simple/example.go).

At a high level:

- API-side enqueue inside a transaction:

```go
runKey, err := flows.BeginTx(ctx, flows.Client{DBConfig: flows.DBConfig{ShardCount: 10}}, tx, myWorkflow, &MyInput{...})
```

- Worker side:

```go
reg := flows.NewRegistry()
flows.Register(reg, myWorkflow)

worker := flows.Worker{Pool: pool, Registry: reg}
_ = worker.Run(ctx)
```

- Workflow code uses durable primitives:

```go
out, err := flows.Execute(ctx, wf, "step/v1", stepFn, in, flows.RetryPolicy{MaxRetries: 3})
flows.Sleep(ctx, wf, "sleep/v1", 5*time.Second)
n := flows.WaitForEvent[int](ctx, wf, "customer_number/v1", "CustomerNumberEvent")
uuid := flows.RandomUUIDv7(ctx, wf, "uuid/v1")
```

## Client APIs

The `Client` provides APIs for managing workflow runs:

```go
client := flows.Client{}

// Get current status of a run
status, err := flows.GetRunStatusTx(ctx, client, tx, runKey)
// status.Status: "queued", "running", "sleeping", "waiting_event", "completed", "failed", "cancelled"

// Cancel a pending run (queued, sleeping, or waiting for event)
err := flows.CancelRunTx(ctx, client, tx, runKey)

// Get the output of a completed run
output, err := flows.GetRunOutputTx[MyOutput](ctx, client, tx, runKey)
```

## Step Execution Options

The `RetryPolicy` supports configurable retries, backoff, and timeouts:

```go
result, err := flows.Execute(ctx, wf, "step/v1", stepFn, in, flows.RetryPolicy{
    MaxRetries:  3,                          // Retry up to 3 times after initial attempt
    Backoff:     func(attempt int) int {     // Wait between retries (milliseconds)
        return 100 * (1 << attempt)          // Exponential: 100ms, 200ms, 400ms
    },
    StepTimeout: 30 * time.Second,           // Timeout per step execution
})
```

**Step Panic Recovery**: If a step panics, the panic is caught and converted to a `StepPanicError`. The step may be retried according to the retry policy.

**Workflow Panic Recovery**: If the workflow function itself panics (outside of `Execute`), the worker catches the panic, marks the run as `failed`, and persists a stack trace in `runs.error_text`.

## Stress test

See [examples/stress/README.md](examples/stress/README.md) for a repeatable stress harness that:
- runs against `citusdata/citus:13.2.0`
- scales worker containers (e.g. 5 containers)
- enqueues A/B/C/D workflows at different volumes
- computes DB-derived completion time stats per workflow (from `runs.created_at` / `runs.updated_at`)
