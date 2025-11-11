-- Flows Database Schema
-- PostgreSQL 17+ with Citus extension for horizontal sharding
-- Sharding Strategy: workflow_name (workflow type with shard suffix)
-- Distribution: Consistent hashing allows adding shards dynamically (starts with 3 shards)
-- Example workflow names: "loan-application-shard-0", "loan-application-shard-1", "loan-application-shard-2"

-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Create UUIDv7 function (time-ordered UUIDs)
-- Based on RFC 9562 draft specification
CREATE OR REPLACE FUNCTION gen_random_uuid_v7()
RETURNS UUID
LANGUAGE plpgsql
VOLATILE
AS $$
DECLARE
    unix_ts_ms BIGINT;
    uuid_bytes BYTEA;
BEGIN
    -- Get current timestamp in milliseconds
    unix_ts_ms := (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT;
    
    -- Generate UUID bytes: 48 bits timestamp + 74 bits random
    uuid_bytes := decode(
        lpad(to_hex(unix_ts_ms), 12, '0') || -- 48 bits timestamp
        encode(gen_random_bytes(10), 'hex'),  -- 80 bits (we'll use 74)
        'hex'
    );
    
    -- Set version (7) and variant bits
    uuid_bytes := set_byte(uuid_bytes, 6, (get_byte(uuid_bytes, 6) & 15) | 112); -- Version 7
    uuid_bytes := set_byte(uuid_bytes, 8, (get_byte(uuid_bytes, 8) & 63) | 128); -- Variant 10
    
    RETURN encode(uuid_bytes, 'hex')::UUID;
END;
$$;

-- Workflows: Main workflow state
-- Distribution column: name (workflow type with shard suffix, e.g., "loan-application-shard-0")
CREATE TABLE IF NOT EXISTS workflows (
    name            TEXT NOT NULL,              -- Distribution column (shard key)
    id              UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    version         INT NOT NULL,
    status          TEXT NOT NULL,              -- pending, running, completed, failed
    input           JSONB,
    output          JSONB,
    error           TEXT,
    sequence_num    INT DEFAULT 0,
    activity_results JSONB DEFAULT '{}'::jsonb,
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (name, id)                      -- Citus requires distribution column in PK
);

-- Indexes: distribution column must be first for local shard routing
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(name, status);
CREATE INDEX IF NOT EXISTS idx_workflows_tenant ON workflows(name, tenant_id);
CREATE INDEX IF NOT EXISTS idx_workflows_updated ON workflows(name, updated_at);

-- Activities: Activity execution state
-- Distribution column: workflow_name (co-located with workflows)
CREATE TABLE IF NOT EXISTS activities (
    workflow_name   TEXT NOT NULL,              -- Distribution column
    workflow_id     UUID NOT NULL,
    id              UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,              -- Activity name
    sequence_num    INT NOT NULL,
    status          TEXT NOT NULL,              -- scheduled, running, completed, failed
    input           JSONB,
    output          JSONB,
    error           TEXT,
    attempt         INT DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    backoff_ms      BIGINT,
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (workflow_name, workflow_id, id),
    FOREIGN KEY (workflow_name, workflow_id) REFERENCES workflows(name, id) ON DELETE CASCADE
);

-- Indexes: distribution column first
CREATE INDEX IF NOT EXISTS idx_activities_workflow ON activities(workflow_name, workflow_id, sequence_num);
CREATE INDEX IF NOT EXISTS idx_activities_retry ON activities(workflow_name, status, next_retry_at) WHERE status = 'scheduled';

-- History Events: Audit log
-- Distribution column: workflow_name (co-located with workflows)
CREATE TABLE IF NOT EXISTS history_events (
    workflow_name   TEXT NOT NULL,              -- Distribution column
    workflow_id     UUID NOT NULL,
    id              UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    sequence_num    INT NOT NULL,
    event_type      TEXT NOT NULL,
    event_data      JSONB,
    PRIMARY KEY (workflow_name, workflow_id, id),
    FOREIGN KEY (workflow_name, workflow_id) REFERENCES workflows(name, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_workflow ON history_events(workflow_name, workflow_id, sequence_num);

-- Timers: Sleep operations
-- Distribution column: workflow_name (co-located with workflows)
CREATE TABLE IF NOT EXISTS timers (
    workflow_name   TEXT NOT NULL,              -- Distribution column
    workflow_id     UUID NOT NULL,
    id              UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    sequence_num    INT NOT NULL,
    fire_at         TIMESTAMPTZ NOT NULL,
    fired           BOOLEAN DEFAULT FALSE,
    PRIMARY KEY (workflow_name, workflow_id, id),
    FOREIGN KEY (workflow_name, workflow_id) REFERENCES workflows(name, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_timers_fire ON timers(workflow_name, fire_at) WHERE NOT fired;
CREATE INDEX IF NOT EXISTS idx_timers_workflow ON timers(workflow_name, workflow_id, sequence_num);

-- Signals: External events
-- Distribution column: workflow_name (co-located with workflows)
CREATE TABLE IF NOT EXISTS signals (
    workflow_name   TEXT NOT NULL,              -- Distribution column
    workflow_id     UUID NOT NULL,
    id              UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    signal_name     TEXT NOT NULL,
    payload         JSONB,
    consumed        BOOLEAN DEFAULT FALSE,
    PRIMARY KEY (workflow_name, workflow_id, id),
    FOREIGN KEY (workflow_name, workflow_id) REFERENCES workflows(name, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_signals_workflow ON signals(workflow_name, workflow_id, signal_name) WHERE NOT consumed;

-- Task Queue: Distributed work queue
-- Distribution column: workflow_name (for efficient worker polling)
CREATE TABLE IF NOT EXISTS task_queue (
    workflow_name       TEXT NOT NULL,          -- Distribution column
    id                  UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    workflow_id         UUID NOT NULL,
    tenant_id           UUID NOT NULL,
    task_type           TEXT NOT NULL,          -- workflow, activity, timer
    task_data           JSONB,
    visibility_timeout  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workflow_name, id),
    FOREIGN KEY (workflow_name, workflow_id) REFERENCES workflows(name, id) ON DELETE CASCADE
);

-- Index for efficient polling: distribution column first
CREATE INDEX IF NOT EXISTS idx_task_queue_poll ON task_queue(workflow_name, visibility_timeout, id);

-- Dead Letter Queue: Failed tasks
-- Distribution column: workflow_name
CREATE TABLE IF NOT EXISTS dead_letter_queue (
    workflow_name           TEXT NOT NULL,      -- Distribution column
    id                      UUID NOT NULL DEFAULT gen_random_uuid_v7(),
    workflow_id             UUID NOT NULL,
    tenant_id               UUID NOT NULL,
    workflow_version        INT NOT NULL,
    input                   JSONB,
    error                   TEXT,
    attempt                 INT,
    metadata                JSONB,
    rerun_as_workflow_id    UUID,
    PRIMARY KEY (workflow_name, id)
);

CREATE INDEX IF NOT EXISTS idx_dlq_workflow ON dead_letter_queue(workflow_name, workflow_id);
CREATE INDEX IF NOT EXISTS idx_dlq_tenant ON dead_letter_queue(workflow_name, tenant_id);
