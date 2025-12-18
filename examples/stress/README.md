# Stress Example

This example provides a repeatable stress harness for Flows on Citus.

It supports two modes:
- `--mode=worker`: run a worker process (intended to be run as multiple containers)
- `--mode=generator`: enqueue a fixed number of workflow runs for A/B/C/D and wait for completion, then print DB-derived completion stats.

## Quick start (Citus + 5 worker containers)

From repo root:

1) Start Postgres (Citus) and 5 worker containers:

```bash
cd examples/stress
export FLOWS_TEST_ID=$(date +%s)
docker compose up --build -d --scale worker=5
```

2) Run the generator on the host (recommended):

```bash
go run ./examples/stress \
  --mode=generator \
  --database-url "postgres://test:test@localhost:5432/flows_test?sslmode=disable" \
  --schema flows \
  --shards 32 \
  --test-id "$FLOWS_TEST_ID" \
  --total-a 100000 \
  --total-b 10000 \
  --total-c 1000 \
  --total-d 100
```

This writes JSON outputs under `examples/stress/results/`.

## Optional: sample docker CPU/memory

If you have Docker CLI on the host:

```bash
go run ./examples/stress \
  --mode=generator \
  --database-url "postgres://test:test@localhost:5432/flows_test?sslmode=disable" \
  --test-id "$FLOWS_TEST_ID" \
  --docker-stats-interval 2s \
  --docker-stats-filter "citus,worker"
```

The docker samples are written to `examples/stress/results/<test_id>_docker_stats.jsonl`.

## Notes

- Each workflow type runs in its own goroutine pool. The `--workers` flag controls the concurrency per workflow type.
- For Citus, the worker queries with an equality predicate on `workflow_name` to enable proper shard routing.
- Completion stats are derived from `runs.created_at` and `runs.updated_at` in the Flows schema.
