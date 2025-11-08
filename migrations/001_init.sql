-- Flows Database Schema
-- PostgreSQL 17+ required for UUIDv7 support

-- Enable pgcrypto for gen_random_bytes
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
CREATE TABLE IF NOT EXISTS workflows (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,
    version         INT NOT NULL,
    status          TEXT NOT NULL,  -- pending, running, completed, failed
    input           JSONB,
    output          JSONB,
    error           TEXT,
    sequence_num    INT DEFAULT 0,
    activity_results JSONB DEFAULT '{}'::jsonb,  -- Cache for replay
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_workflows_tenant_status ON workflows(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_workflows_tenant_name ON workflows(tenant_id, name);

-- Activities: Activity execution state
CREATE TABLE IF NOT EXISTS activities (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    sequence_num    INT NOT NULL,
    status          TEXT NOT NULL,  -- scheduled, running, completed, failed
    input           JSONB,
    output          JSONB,
    error           TEXT,
    attempt         INT DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    backoff_ms      BIGINT,
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_activities_workflow ON activities(tenant_id, workflow_id, sequence_num);
CREATE INDEX IF NOT EXISTS idx_activities_retry ON activities(tenant_id, status, next_retry_at) WHERE status = 'scheduled';

-- History Events: Audit log
CREATE TABLE IF NOT EXISTS history_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    sequence_num    INT NOT NULL,
    event_type      TEXT NOT NULL,  -- WORKFLOW_STARTED, ACTIVITY_SCHEDULED, ACTIVITY_COMPLETED, etc.
    event_data      JSONB
);
CREATE INDEX IF NOT EXISTS idx_history_workflow ON history_events(tenant_id, workflow_id, sequence_num);

-- Timers: Sleep operations
CREATE TABLE IF NOT EXISTS timers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    sequence_num    INT NOT NULL,
    fire_at         TIMESTAMPTZ NOT NULL,
    fired           BOOLEAN DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_timers_fire ON timers(tenant_id, fire_at) WHERE NOT fired;
CREATE INDEX IF NOT EXISTS idx_timers_workflow ON timers(tenant_id, workflow_id, sequence_num);

-- Signals: External events
CREATE TABLE IF NOT EXISTS signals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    signal_name     TEXT NOT NULL,
    payload         JSONB,
    consumed        BOOLEAN DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_signals_workflow ON signals(tenant_id, workflow_id, signal_name) WHERE NOT consumed;

-- Task Queue: Distributed work queue
CREATE TABLE IF NOT EXISTS task_queue (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id           UUID NOT NULL,
    workflow_id         UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    workflow_name       TEXT NOT NULL,  -- For worker type filtering
    task_type           TEXT NOT NULL,  -- workflow, activity, timer
    task_data           JSONB,          -- Additional task-specific data
    visibility_timeout  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_queue_poll ON task_queue(tenant_id, workflow_name, visibility_timeout);

-- Dead Letter Queue: Failed tasks
CREATE TABLE IF NOT EXISTS dead_letter_queue (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id               UUID NOT NULL,
    workflow_id             UUID NOT NULL,
    workflow_name           TEXT NOT NULL,
    workflow_version        INT NOT NULL,
    input                   JSONB,
    error                   TEXT,
    attempt                 INT,
    metadata                JSONB,  -- {dlq_id, original_workflow_id, attempt}
    rerun_as_workflow_id    UUID    -- Set when rerun from DLQ
);
CREATE INDEX IF NOT EXISTS idx_dlq_tenant ON dead_letter_queue(tenant_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_dlq_workflow ON dead_letter_queue(tenant_id, workflow_id);
