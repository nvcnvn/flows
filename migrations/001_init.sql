-- Flows Database Schema
-- PostgreSQL 18+ required for UUIDv7 support

-- Workflows: Main workflow state
CREATE TABLE workflows (
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
CREATE INDEX idx_workflows_tenant_status ON workflows(tenant_id, status);
CREATE INDEX idx_workflows_tenant_name ON workflows(tenant_id, name);

-- Activities: Activity execution state
CREATE TABLE activities (
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
CREATE INDEX idx_activities_workflow ON activities(tenant_id, workflow_id, sequence_num);
CREATE INDEX idx_activities_retry ON activities(tenant_id, status, next_retry_at) WHERE status = 'scheduled';

-- History Events: Audit log
CREATE TABLE history_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    sequence_num    INT NOT NULL,
    event_type      TEXT NOT NULL,  -- WORKFLOW_STARTED, ACTIVITY_SCHEDULED, ACTIVITY_COMPLETED, etc.
    event_data      JSONB
);
CREATE INDEX idx_history_workflow ON history_events(tenant_id, workflow_id, sequence_num);

-- Timers: Sleep operations
CREATE TABLE timers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    sequence_num    INT NOT NULL,
    fire_at         TIMESTAMPTZ NOT NULL,
    fired           BOOLEAN DEFAULT FALSE
);
CREATE INDEX idx_timers_fire ON timers(tenant_id, fire_at) WHERE NOT fired;
CREATE INDEX idx_timers_workflow ON timers(tenant_id, workflow_id, sequence_num);

-- Signals: External events
CREATE TABLE signals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id       UUID NOT NULL,
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    signal_name     TEXT NOT NULL,
    payload         JSONB,
    consumed        BOOLEAN DEFAULT FALSE
);
CREATE INDEX idx_signals_workflow ON signals(tenant_id, workflow_id, signal_name) WHERE NOT consumed;

-- Task Queue: Distributed work queue
CREATE TABLE task_queue (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid_v7(),
    tenant_id           UUID NOT NULL,
    workflow_id         UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    workflow_name       TEXT NOT NULL,  -- For worker type filtering
    task_type           TEXT NOT NULL,  -- workflow, activity, timer
    task_data           JSONB,          -- Additional task-specific data
    visibility_timeout  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_task_queue_poll ON task_queue(tenant_id, workflow_name, visibility_timeout);

-- Dead Letter Queue: Failed tasks
CREATE TABLE dead_letter_queue (
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
CREATE INDEX idx_dlq_tenant ON dead_letter_queue(tenant_id, id DESC);
CREATE INDEX idx_dlq_workflow ON dead_letter_queue(tenant_id, workflow_id);
