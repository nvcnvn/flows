# GitHub Copilot Instructions for Flows Project

## Parallel Testing Best Practices

### Problem Overview

When writing tests for workflow engines that use shared database resources (like this project), parallel test execution (`go test -parallel N`) can cause race conditions and failures if not properly designed. The library is meant to run in production with multiple processes sharing the same database, so tests must simulate this environment correctly.

### Common Issues with Parallel Tests

1. **"closed pool" errors**: Tests closing database connections that other parallel tests are still using
2. **Shared global counters**: Activity retry counters being incremented by multiple parallel tests simultaneously
3. **Race conditions**: Tests interfering with each other's state

### Solution: Shared Database Pool Pattern

#### Implementation

```go
// tests/utils_test.go
var (
    sharedPool     *pgxpool.Pool
    sharedPoolOnce sync.Once
    sharedPoolMu   sync.Mutex
)

// SetupTestDB returns a shared database connection pool for all tests
// This simulates production where multiple processes share the same database
func SetupTestDB(t *testing.T) *pgxpool.Pool {
    sharedPoolMu.Lock()
    defer sharedPoolMu.Unlock()

    sharedPoolOnce.Do(func() {
        ctx := context.Background()

        // Get connection string from environment
        connString := os.Getenv("DATABASE_URL")
        if connString == "" {
            connString = "postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable"
        }

        // Connect to database
        pool, err := pgxpool.New(ctx, connString)
        if err != nil {
            panic("Failed to connect to database: " + err.Error())
        }

        // Verify connection with retries (for CI environments)
        maxRetries := 10
        for i := 0; i < maxRetries; i++ {
            err = pool.Ping(ctx)
            if err == nil {
                break
            }
            if i == maxRetries-1 {
                panic("Failed to ping database after retries: " + err.Error())
            }
            time.Sleep(2 * time.Second)
        }

        sharedPool = pool
    })

    return sharedPool
}
```

**Key Points:**
- Use `sync.Once` to ensure pool is created only once
- Pool is shared across ALL tests (simulates production)
- Pool is never closed during test execution (lives for entire test suite)
- Tests can run in any order without conflicts

### Solution: Per-Test Counter Isolation

#### Problem
Global counters like this cause tests to interfere:

```go
// ❌ BAD: Global counter shared by all tests
var attemptCounter int32

var MyActivity = flows.NewActivity(
    "my-activity",
    func(ctx context.Context, input *MyInput) (*MyOutput, error) {
        attempt := atomic.AddInt32(&attemptCounter, 1)  // All tests increment same counter!
        // ...
    },
)
```

#### Solution: Per-Test Counter Map

```go
// ✅ GOOD: Per-test counter isolation
var (
    retryAttemptCounters   = make(map[string]*int32)
    retryAttemptCountersMu sync.Mutex
)

func getRetryAttemptCounter(testID string) *int32 {
    retryAttemptCountersMu.Lock()
    defer retryAttemptCountersMu.Unlock()
    
    if counter, exists := retryAttemptCounters[testID]; exists {
        return counter
    }
    
    var counter int32
    retryAttemptCounters[testID] = &counter
    return &counter
}

func resetRetryAttemptCounter(testID string) {
    retryAttemptCountersMu.Lock()
    defer retryAttemptCountersMu.Unlock()
    delete(retryAttemptCounters, testID)
}

// In your test:
func TestMyActivity(t *testing.T) {
    t.Parallel()  // Can safely run in parallel now
    
    testID := uuid.New().String()
    resetRetryAttemptCounter(testID)
    
    exec, err := flows.Start(ctx, MyWorkflow, &MyInput{
        TestID: testID,  // Pass test ID to identify this test's counter
        // ...
    })
    // ...
}
```

### Test Structure Requirements

1. **Mark tests as parallel:**
   ```go
   func TestMyFeature(t *testing.T) {
       t.Parallel()  // Enable parallel execution
       // ...
   }
   ```

2. **Use unique identifiers per test:**
   - Generate unique test IDs: `testID := uuid.New().String()`
   - Pass to workflows/activities that use counters
   - Use for database tenant isolation: `ctx = flows.WithTenantID(ctx, uuid.New())`

3. **Isolate test data:**
   - Each test should use unique tenant IDs
   - Reset only YOUR test's counters, not global state
   - Use unique job IDs, workflow IDs, etc.

### Verification Commands

Always verify tests work with parallel execution:

```bash
# Test with default parallelism
go test -v ./...

# Test with explicit parallelism (matches CI)
go test -v -parallel 4 ./...

# Test with race detection
go test -v -race -parallel 4 ./...

# Full CI-like test
go test -v -parallel 4 -race -coverprofile=coverage.txt -covermode=atomic ./...
```

### CI Configuration

The `.github/workflows/ci.yml` runs tests with:
- `-parallel 4`: Run up to 4 tests simultaneously
- `-race`: Enable race detector
- Shared PostgreSQL database (simulates production)

### Checklist for New Tests

When writing new tests, ensure:

- [ ] Test uses `t.Parallel()` if it can run concurrently
- [ ] Test calls `SetupTestDB(t)` for database access (shared pool)
- [ ] Test uses unique tenant ID: `ctx = flows.WithTenantID(ctx, uuid.New())`
- [ ] Any counters/state are isolated per-test (use maps keyed by test ID)
- [ ] Test works when run alone: `go test -v -run TestName`
- [ ] Test works in parallel: `go test -v -parallel 4 ./...`
- [ ] Test works with race detector: `go test -v -race -run TestName`

### Anti-Patterns to Avoid

❌ **Creating new pool per test:**
```go
func TestBad(t *testing.T) {
    pool := createNewPool()  // Each test gets own pool
    t.Cleanup(func() {
        pool.Close()  // Closes while other tests might still use it
    })
}
```

❌ **Global shared counters:**
```go
var globalCounter int32  // All parallel tests increment this
```

❌ **Test-level cleanup that affects others:**
```go
t.Cleanup(func() {
    truncateAllTables()  // Breaks other running tests!
})
```

✅ **Use shared pool, per-test isolation:**
```go
func TestGood(t *testing.T) {
    t.Parallel()
    pool := SetupTestDB(t)  // Shared pool, safe
    tenantID := uuid.New()   // Unique per test
    testID := uuid.New().String()  // Unique identifier
    resetMyTestCounter(testID)  // Only reset this test's counter
}
```

### Debugging Parallel Test Issues

If you see intermittent test failures:

1. **Run tests sequentially first:**
   ```bash
   go test -v -p 1 ./...
   ```

2. **If sequential works but parallel fails:**
   - Check for shared state (global variables, counters)
   - Check for database pool management issues
   - Look for race conditions with `-race`

3. **Check for specific error patterns:**
   - "closed pool" → Pool being closed while tests still running
   - Inconsistent counter values → Global counter not isolated
   - "context deadline exceeded" → Tests waiting on resources held by other tests

### Summary

The key principle is: **Tests should simulate production where multiple processes share database resources, but each test should have isolated state for deterministic results.**

- **Shared:** Database connection pool (via `SetupTestDB`)
- **Isolated:** Tenant IDs, test counters, workflow instances, test data
