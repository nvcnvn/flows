# Quick Start Guide

Get the demo running in under 5 minutes!

## Option 1: Docker Compose (Recommended)

```bash
# From the demo directory
docker-compose up -d

# Wait a few seconds for services to start
sleep 5

# Run the example script
./examples.sh
```

Done! The script will walk through a complete loan application.

## Option 2: Local Development

### 1. Start PostgreSQL

```bash
# From repository root
docker-compose up -d postgres

# Or use existing PostgreSQL and run migrations
psql -h localhost -p 5433 -U postgres -d flows_test -f ../migrations/001_init.sql
```

### 2. Run the Demo

```bash
cd demo
DATABASE_URL="postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable" go run .
```

### 3. Try the API

In another terminal:

```bash
cd demo
./examples.sh
```

## What Happens?

The script will:

1. **Start a loan application** ($75,000 for home renovation)
2. **Credit check** runs automatically (activity)
3. **Documents submitted** via signals (identity, income, address)
4. **Compliance check** waits 5 seconds (timer/sleep)
5. **Random review** may trigger (30% chance - deterministic!)
6. **Manager approval** sent if needed (signal)
7. **Loan disbursed** (final activity)
8. **Result returned** (approved/rejected)

**Note on API Structure:** All API calls use the **base workflow name** (`loan-application`) in the URL path. The library automatically handles sharding internally using consistent hashing - you never need to worry about shard suffixes!

## Explore the Database

While the workflow runs, connect to PostgreSQL and run queries:

```bash
psql -h localhost -p 5433 -U postgres -d flows_test
```

```sql
-- See all workflows
SELECT id, name, status, input->>'applicant_name' as applicant
FROM workflows 
ORDER BY updated_at DESC 
LIMIT 5;

-- See activities for a workflow
SELECT name, status, sequence_num, attempt 
FROM activities 
WHERE workflow_id = 'YOUR_WORKFLOW_ID' 
ORDER BY sequence_num;

-- See signals
SELECT signal_name, consumed 
FROM signals 
WHERE workflow_id = 'YOUR_WORKFLOW_ID';

-- See history
SELECT sequence_num, event_type 
FROM history_events 
WHERE workflow_id = 'YOUR_WORKFLOW_ID' 
ORDER BY sequence_num;
```

## API Testing

### Manual Testing with curl

```bash
# 1. Start application
curl -X POST http://localhost:8181/api/loans \
  -H "Content-Type: application/json" \
  -d '{"applicant_name":"John Doe","amount":50000,"purpose":"business"}'

# Save the workflow_id and workflow_name from response
# Note: workflow_name is the base name (e.g., "loan-application")
# The API automatically handles sharding internally

# 2. Check status (use base workflow name in path)
curl "http://localhost:8181/api/loans/loan-application/WORKFLOW_ID"

# 3. Submit documents
curl -X POST "http://localhost:8181/api/loans/loan-application/WORKFLOW_ID/documents" \
  -H "Content-Type: application/json" \
  -d '{"document_type":"identity","document_id":"DL-123"}'

# 4. Get result
curl "http://localhost:8181/api/loans/loan-application/WORKFLOW_ID/result"
```

## Troubleshooting

### "connection refused"
- Check if PostgreSQL is running: `docker ps`
- Check if API is running: `curl http://localhost:8181/health`

### "workflow not found"
- Make sure you're using the correct `workflow_name` (base name) from the start response
- Verify you have the correct `workflow_id`
- Check that the tenant ID matches (in multi-tenant scenarios)

### "database does not exist"
- Run migrations: `psql -h localhost -p 5433 -U postgres -d flows_test -f ../migrations/001_init.sql`

### Worker not processing tasks
- Check worker is running (part of main.go)
- Check task queue: `SELECT * FROM task_queue LIMIT 10;`
- Check worker logs in console

## Next Steps

- Read `README.md` for detailed explanation of each step
- Explore different loan amounts (< $50k, $50k-$100k, > $100k)
- Try rejection scenarios (check credit score generation in activities.go)
- Experiment with the 30% random review trigger
- Check the database state at each step

## Architecture

```
┌─────────────┐      ┌──────────────┐
│   REST API  │──────│  PostgreSQL  │
│  (Go 1.22)  │      │   (Flows DB) │
└─────────────┘      └──────────────┘
       │                    │
       │                    │
       ▼                    ▼
┌─────────────┐      ┌──────────────┐
│   Workflow  │      │    Worker    │
│   Engine    │◄─────│  (Executor)  │
└─────────────┘      └──────────────┘
```

The worker polls the database for tasks and executes workflows/activities.
All state is stored in PostgreSQL for durability and consistency.

Happy workflow building! 🎉
