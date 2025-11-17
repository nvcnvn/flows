# Flows Demo: Loan Application Workflow

This demo application showcases all features of the **flows** workflow engine through a realistic loan application system. It demonstrates:

- ✨ **UUIDv7** generation (time-ordered UUIDs)
- 🔄 **Activities** (external service calls with retries)
- 📨 **Signals** (waiting for external events)
- ⏰ **Timers** (Sleep operations)
- 🎲 **Random** (deterministic randomness)
- 🔀 **Branching** (conditional logic)
- 🔁 **Loops** (document collection)
- 🗄️ **Database operations** (complete workflow state management)

## Architecture

The application is a REST API built with Go 1.22+'s new HTTP mux pattern. It consists of:

1. **Workflow**: `LoanApplicationWorkflow` - Orchestrates the entire loan approval process
2. **Activities**: External service calls (credit check, document verification, disbursement)
3. **HTTP API**: REST endpoints to start workflows and send signals
4. **Worker**: Background process that executes workflow tasks

## Prerequisites

- Go 1.22+
- PostgreSQL 13+ (or Docker)
- Database initialized with migrations from `../migrations/001_init.sql`

## Quick Start

### 1. Start PostgreSQL

```bash
# Using Docker Compose (from repository root)
docker-compose up -d

# Or use your own PostgreSQL instance
# Make sure to run migrations first:
psql -h localhost -p 5433 -U postgres -d flows_test -f ../migrations/001_init.sql
```

### 2. Run the Demo Application

```bash
cd demo
DATABASE_URL="postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable" go run .
```

The API will start on `http://localhost:8181`

### 3. Submit a Loan Application

```bash
curl -X POST http://localhost:8181/api/loans \
  -H "Content-Type: application/json" \
  -d '{
    "applicant_name": "Jane Smith",
    "amount": 75000,
    "purpose": "home renovation"
  }'
```

Response:
```json
{
  "workflow_id": "018c123e-4567-7890-abcd-ef0123456789",
  "workflow_name": "loan-application",
  "message": "Loan application started successfully (shard: loan-application-shard-1)"
}
```

**Understanding Workflow Names & Sharding:**

The response includes both:
- `workflow_name`: Base name you defined (`"loan-application"`) - use this for all API calls
- `message`: Contains the sharded name (`loan-application-shard-1`) for debugging

**Sharding is transparent**: The library automatically distributes workflows across shards using consistent hashing based on the workflow ID. You always use the base name in your code - the shard suffix is added internally and shown only for observability.

**Database State After This Step:**

Query the database to see what was created:

```sql
-- Workflow record created
SELECT id, name, status, tenant_id, input 
FROM workflows 
WHERE id = '018c123e-4567-7890-abcd-ef0123456789';

-- Result:
-- id: 018c123e-4567-7890-abcd-ef0123456789 (UUIDv7, time-ordered)
-- name: loan-application-shard-1 (sharded based on hash)
-- status: running
-- tenant_id: <unique tenant UUID>
-- input: {"applicant_name":"Jane Smith","amount":75000,"purpose":"home renovation"}

-- Activity for credit score check is created
SELECT id, name, status, sequence_num, input 
FROM activities 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789';

-- Result:
-- id: <activity UUID>
-- name: check-credit-score
-- status: scheduled (or completed if worker already processed it)
-- sequence_num: 1
-- input: {"applicant_name":"Jane Smith"}

-- Task queue entry for worker
SELECT id, task_type, workflow_id 
FROM task_queue 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789';

-- Result:
-- id: <task UUID>
-- task_type: activity
-- workflow_id: 018c123e-4567-7890-abcd-ef0123456789
```

### 4. Check Status

```bash
# Use the base workflow name "loan-application" and workflow_id from previous response
curl "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789"
```

Response shows workflow is waiting for documents:
```json
{
  "ID": "018c123e-4567-7890-abcd-ef0123456789",
  "TenantID": "...",
  "Name": "loan-application-shard-1",
  "Version": 1,
  "Status": "running",
  "Error": ""
}
```

**Note:** The database stores the sharded name (`loan-application-shard-1`), but you always use the base name (`loan-application`) in API calls. The library derives the correct shard internally using consistent hashing.

**Database State After Credit Check:**

```sql
-- Credit score activity completed
SELECT name, status, output, attempt 
FROM activities 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND name = 'check-credit-score';

-- Result:
-- name: check-credit-score
-- status: completed
-- output: {"score":720,"provider":"Demo Credit Bureau","checked_at":"2024-01-15T10:30:00Z"}
-- attempt: 0 (succeeded on first try)

-- Workflow is re-enqueued to continue execution
SELECT id, task_type 
FROM task_queue 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND task_type = 'workflow';

-- History event recorded
SELECT event_type, event_data 
FROM history_events 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
ORDER BY sequence_num;

-- Results show workflow started and activity completed events
```

### 5. Submit Required Documents

The workflow is now waiting for documents. For amounts > $50,000, it requires: identity, income, and address.

```bash
# Submit identity document
curl -X POST "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789/documents" \
  -H "Content-Type: application/json" \
  -d '{
    "document_type": "identity",
    "document_id": "DL-987654321"
  }'

# Submit income document
curl -X POST "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789/documents" \
  -H "Content-Type: application/json" \
  -d '{
    "document_type": "income",
    "document_id": "W2-2023-456"
  }'

# Submit address document (required for amounts > $50k)
curl -X POST "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789/documents" \
  -H "Content-Type: application/json" \
  -d '{
    "document_type": "address",
    "document_id": "UTIL-202401-789"
  }'
```

**Database State After Sending Signals:**

```sql
-- Signals are stored
SELECT signal_name, payload, consumed 
FROM signals 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
ORDER BY signal_name;

-- Results:
-- signal_name: document-address
-- payload: {"document_type":"address","document_id":"UTIL-202401-789","uploaded_at":"..."}
-- consumed: true (after workflow processes it)

-- signal_name: document-identity
-- payload: {"document_type":"identity","document_id":"DL-987654321","uploaded_at":"..."}
-- consumed: true

-- signal_name: document-income
-- payload: {"document_type":"income","document_id":"W2-2023-456","uploaded_at":"..."}
-- consumed: true

-- Document verification activities created
SELECT name, status, input, output 
FROM activities 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND name = 'verify-document' 
ORDER BY sequence_num;

-- Results show 3 verification activities (one for each document)
```

### 6. Wait for Compliance Check (Timer)

The workflow now sleeps for 5 seconds (compliance check period).

**Database State During Sleep:**

```sql
-- Timer created
SELECT id, fire_at, fired, sequence_num 
FROM timers 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789';

-- Result:
-- id: <timer UUID>
-- fire_at: 2024-01-15T10:35:05Z (5 seconds from creation)
-- fired: false (becomes true after timer fires)
-- sequence_num: 8

-- Timer task in queue
SELECT id, task_type, visibility_timeout 
FROM task_queue 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND task_type = 'timer';

-- Result:
-- visibility_timeout: matches fire_at time
-- Worker won't process this until that time
```

### 7. Handle Random Review (30% chance)

The workflow uses `Random()` to decide if additional review is needed. This is deterministic - the same workflow execution will always get the same random result, even on replay!

**Database State for Random Generation:**

```sql
-- Random bytes are recorded in history
SELECT event_type, event_data 
FROM history_events 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND event_type = 'random_generated';

-- Result:
-- event_type: random_generated
-- event_data: {"random_bytes":[142,23,67,...],"num_bytes":8}

-- This ensures deterministic replay - same random value every time!
```

If additional review is triggered (30% chance), send manager approval:

```bash
curl -X POST "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789/approve" \
  -H "Content-Type: application/json" \
  -d '{
    "approver_role": "manager",
    "approved": true,
    "comments": "Application looks good, approve"
  }'
```

**Database State for Manager Approval:**

```sql
-- Manager approval signal stored
SELECT signal_name, payload, consumed 
FROM signals 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND signal_name = 'manager-approval';

-- Result:
-- signal_name: manager-approval
-- payload: {"approver_role":"manager","approved":true,"comments":"...","approved_at":"..."}
-- consumed: true
```

### 8. Large Loan Underwriter Check (for amounts > $100k)

For loans over $100,000, an underwriter activity is executed automatically.

**Database State for Underwriter:**

```sql
-- Underwriter activity (only created if amount > $100k)
SELECT name, status, input, output 
FROM activities 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND name = 'get-underwriter-approval';

-- Result (if amount > $100k):
-- name: get-underwriter-approval
-- status: completed
-- input: {"application_id":"...","amount":150000,"credit_score":720}
-- output: {"approved":true,"reason":"Excellent credit history..."}
```

### 9. Final Disbursement

The workflow executes the loan disbursement activity.

**Database State for Disbursement:**

```sql
-- Disbursement activity
SELECT name, status, input, output 
FROM activities 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
  AND name = 'disburse-loan';

-- Result:
-- name: disburse-loan
-- status: completed
-- input: {"application_id":"...","applicant_name":"Jane Smith","amount":75000}
-- output: {"transaction_id":"<UUID>","disbursed_at":"2024-01-15T10:35:30Z","amount":75000}
```

### 10. Get Final Result

```bash
curl "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789/result"
```

Response:
```json
{
  "ID": "018c123e-4567-7890-abcd-ef0123456789",
  "TenantID": "...",
  "Name": "loan-application-shard-1",
  "Version": 1,
  "Status": "completed",
  "Error": ""
}
```

**Final Database State:**

```sql
-- Workflow completed
SELECT id, status, output, sequence_num, activity_results 
FROM workflows 
WHERE id = '018c123e-4567-7890-abcd-ef0123456789';

-- Result:
-- id: 018c123e-4567-7890-abcd-ef0123456789
-- status: completed
-- output: {"application_id":"...","decision":"approved","approved_amount":75000,"reason":"All checks passed successfully","processed_at":"2024-01-15T10:35:30Z"}
-- sequence_num: 15 (final sequence number after all operations)
-- activity_results: JSON map of all activity outputs by sequence number

-- All activities completed
SELECT name, status, sequence_num 
FROM activities 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
ORDER BY sequence_num;

-- Results:
-- 1: check-credit-score (completed)
-- 2: verify-document (completed - identity)
-- 3: verify-document (completed - income)
-- 4: verify-document (completed - address)
-- 5: disburse-loan (completed)

-- All signals consumed
SELECT signal_name, consumed 
FROM signals 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789';

-- All signals show consumed: true

-- Timer fired
SELECT fired 
FROM timers 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789';

-- fired: true

-- Complete history available for audit
SELECT sequence_num, event_type 
FROM history_events 
WHERE workflow_id = '018c123e-4567-7890-abcd-ef0123456789' 
ORDER BY sequence_num;

-- Shows complete audit trail of all workflow operations
```

## API Endpoints

All endpoints use the **base workflow name** (`loan-application`) in the path, not the sharded name. Sharding is handled transparently.

### POST /api/loans
Start a new loan application workflow.

**Request:**
```json
{
  "applicant_name": "John Doe",
  "amount": 50000,
  "purpose": "business expansion"
}
```

**Response:**
```json
{
  "workflow_id": "018c...",
  "workflow_name": "loan-application",
  "message": "Loan application started successfully (shard: loan-application-shard-0)"
}
```

### GET /api/loans/{workflowName}/{id}
Get workflow status using the base workflow name.

**Example:**
```bash
curl "http://localhost:8181/api/loans/loan-application/018c123e-4567-7890-abcd-ef0123456789"
```

**Response:**
```json
{
  "ID": "018c...",
  "TenantID": "...",
  "Name": "loan-application-shard-0",
  "Version": 1,
  "Status": "running",
  "Error": ""
}
```

### POST /api/loans/{workflowName}/{id}/documents
Submit a document (sends signal).

**Example:**
```bash
curl -X POST "http://localhost:8181/api/loans/loan-application/018c.../documents" \
  -H "Content-Type: application/json" \
  -d '{"document_type":"identity","document_id":"DL-123456789"}'
```

### POST /api/loans/{workflowName}/{id}/approve
Submit manager approval (sends signal).

**Example:**
```bash
curl -X POST "http://localhost:8181/api/loans/loan-application/018c.../approve" \
  -H "Content-Type: application/json" \
  -d '{"approver_role":"manager","approved":true,"comments":"Approved"}'
```

### GET /api/loans/{workflowName}/{id}/result
Get final workflow result.

**Example:**
```bash
curl "http://localhost:8181/api/loans/loan-application/018c.../result"
```

## Database Schema Deep Dive

All tables are sharded by `workflow_name` for horizontal scalability. See `migrations/001_init.sql` for complete schema.

### workflows
- Stores main workflow state
- `name`: Shard key (e.g., "loan-application-shard-0")
- `id`: UUIDv7 (time-ordered)
- `status`: pending, running, completed, failed
- `input`/`output`: JSONB
- `activity_results`: JSONB map of activity outputs by sequence number

### activities
- Co-located with workflows (same shard key)
- Each activity execution is a row
- `status`: scheduled, running, completed, failed
- `attempt`: Retry counter
- `next_retry_at`: When to retry if failed

### history_events
- Audit trail of all workflow operations
- Includes: workflow started, activity completed, timer fired, random generated, time recorded
- Ordered by `sequence_num`

### timers
- Sleep operations
- Worker polls for timers with `fire_at <= NOW()`
- `fired`: Tracks if timer has executed

### signals
- External events workflow is waiting for
- `consumed`: false until workflow processes the signal
- Multiple signals can be pending

### task_queue
- Distributed work queue for worker
- `task_type`: workflow, activity, timer
- `visibility_timeout`: Task lock mechanism

## Debugging Tips

### 1. Check Workflow Status
```sql
SELECT id, status, error, sequence_num 
FROM workflows 
WHERE id = '<workflow_id>';
```

### 2. See What Step Workflow Is On
```sql
SELECT sequence_num, name, status 
FROM activities 
WHERE workflow_id = '<workflow_id>' 
ORDER BY sequence_num DESC 
LIMIT 1;
```

### 3. Check Pending Signals
```sql
SELECT signal_name, consumed 
FROM signals 
WHERE workflow_id = '<workflow_id>' 
  AND NOT consumed;
```

### 4. View Activity Failures
```sql
SELECT name, attempt, error 
FROM activities 
WHERE workflow_id = '<workflow_id>' 
  AND status = 'failed';
```

### 5. Check Timer Status
```sql
SELECT fire_at, fired 
FROM timers 
WHERE workflow_id = '<workflow_id>';
```

### 6. View Complete History
```sql
SELECT sequence_num, event_type, event_data 
FROM history_events 
WHERE workflow_id = '<workflow_id>' 
ORDER BY sequence_num;
```

## Key Features Demonstrated

### Deterministic Execution
- All random values are recorded in `history_events`
- Workflow replay will produce exactly the same results
- Time operations are also recorded for determinism

### Fault Tolerance
- Activities have automatic retry with exponential backoff
- Worker crashes don't lose work (tasks remain in queue)
- Database transactions ensure consistency

### Scalability
- Horizontal sharding by workflow name
- Workers can be scaled independently
- No single point of contention

### Observability
- Complete audit trail in `history_events`
- Activity status and retry attempts tracked
- Easy to see what workflow is waiting for

## Workflow Sharding Explained

The flows library uses **transparent sharding** to horizontally scale workflow execution across database shards. Here's how it works:

### How Sharding Works

1. **Base Workflow Name**: You define workflows with simple names like `"loan-application"`
2. **Automatic Distribution**: When starting a workflow, the library:
   - Generates a UUIDv7 for the workflow
   - Uses consistent hashing on the workflow ID to select a shard (0 to N-1)
   - Appends the shard suffix: `"loan-application"` → `"loan-application-shard-1"`
3. **Transparent API**: All API calls use the base name - sharding happens internally

### Example

```go
// Define workflow with base name
var LoanApplicationWorkflow = flows.NewWorkflow(
    "loan-application",  // Base name (no shard suffix)
    loanWorkflowFunc,
)

// Start workflow
exec, err := flows.Start(ctx, LoanApplicationWorkflow, input)

// API uses base name
exec.WorkflowName()        // Returns: "loan-application"
exec.WorkflowNameSharded() // Returns: "loan-application-shard-1" (for debugging)

// All operations use base name
flows.SendSignal(ctx, exec.WorkflowName(), exec.WorkflowID(), "signal", payload)
flows.Query(ctx, exec.WorkflowName(), exec.WorkflowID())
```

### Why Sharding?

- **Horizontal Scalability**: Distribute load across multiple database shards (Citus, distributed PostgreSQL)
- **Reduced Contention**: Each shard handles a subset of workflows independently
- **Consistent Hashing**: Uses virtual nodes for balanced distribution when adding/removing shards

### Shard Assignment

- **Deterministic**: Same workflow ID always maps to the same shard
- **Persistent**: Shard assignment never changes for a given workflow ID
- **Load Balanced**: Consistent hashing ensures even distribution

### Configuration

Default: 3 shards. Configure before starting workflows:

```go
flows.SetShardConfig(&flows.ShardConfig{
    NumShards: 10, // Increase for higher throughput
})
```

### Worker Configuration

Workers must poll **all shards** for their workflow type:

```go
worker := flows.NewWorker(pool, flows.WorkerConfig{
    WorkflowNames: []string{"loan-application"}, // Base name only
    // Worker automatically polls: loan-application-shard-0, loan-application-shard-1, loan-application-shard-2
})
```

The worker internally expands base names to all sharded variants using `flows.GetAllShardedWorkflowNames()`.

## Production Considerations

1. **Sharding**: 
   - Default: 3 shards (suitable for development)
   - Production: 10-100 shards based on load
   - Configure `NumShards` before any workflows start
   - Never change after workflows are created (shard assignment is permanent)

2. **Workers**: 
   - Run multiple worker processes for throughput
   - Each worker polls all shards for registered workflow types
   - Scale workers independently of shards

3. **Monitoring**: 
   - Add metrics for task queue depth per shard
   - Track activity failures and workflow duration
   - Monitor shard distribution balance

4. **Dead Letter Queue**: 
   - Check `dead_letter_queue` table for failed workflows
   - DLQ entries include the sharded workflow name

5. **Tenant Isolation**: 
   - Use `tenant_id` for multi-tenant deployments
   - Each tenant's workflows are isolated by `tenant_id` + `workflow_name` (sharded)

6. **Database**: 
   - Use Citus for true distributed sharding (recommended for production)
   - Regular PostgreSQL works but sharding is logical (same performance)
   - Citus distributes shards across physical nodes for true horizontal scaling

## License

MIT
