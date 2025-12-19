CREATE SCHEMA IF NOT EXISTS "flows";

CREATE TABLE IF NOT EXISTS "flows"."runs" (
        workflow_name_shard text NOT NULL,
        run_id             uuid NOT NULL,
        workflow_name      text NOT NULL,
        status        text NOT NULL,
        input_json    jsonb NOT NULL,
        output_json   jsonb,
        error_text    text,
        next_wake_at  timestamptz,
        created_at    timestamptz NOT NULL DEFAULT now(),
        updated_at    timestamptz NOT NULL DEFAULT now(),
        PRIMARY KEY (workflow_name_shard, run_id)
);

CREATE INDEX IF NOT EXISTS runs_runnable_idx
        ON "flows"."runs" (workflow_name_shard, status, next_wake_at, created_at);

CREATE TABLE IF NOT EXISTS "flows"."steps" (
        workflow_name_shard text NOT NULL,
        run_id              uuid NOT NULL,
        step_key    text NOT NULL,
        status      text NOT NULL,
        input_json  jsonb,
        output_json jsonb,
        error_text  text,
        attempts    int NOT NULL DEFAULT 0,
        created_at  timestamptz NOT NULL DEFAULT now(),
        updated_at  timestamptz NOT NULL DEFAULT now(),
        PRIMARY KEY (workflow_name_shard, run_id, step_key),
        FOREIGN KEY (workflow_name_shard, run_id)
                REFERENCES "flows"."runs"(workflow_name_shard, run_id)
                ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "flows"."waits" (
        workflow_name_shard text NOT NULL,
        run_id              uuid NOT NULL,
        wait_key     text NOT NULL,
        wait_type    text NOT NULL,
        event_name   text,
        wake_at      timestamptz,
        payload_json jsonb,
        satisfied_at timestamptz,
        created_at   timestamptz NOT NULL DEFAULT now(),
        updated_at   timestamptz NOT NULL DEFAULT now(),
        PRIMARY KEY (workflow_name_shard, run_id, wait_key),
        FOREIGN KEY (workflow_name_shard, run_id)
                REFERENCES "flows"."runs"(workflow_name_shard, run_id)
                ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS waits_event_idx
        ON "flows"."waits" (workflow_name_shard, event_name, satisfied_at);

-- One event per (run,event_name). If you need multiple events, add an event_id and remove this PK.
CREATE TABLE IF NOT EXISTS "flows"."events" (
        workflow_name_shard text NOT NULL,
        run_id              uuid NOT NULL,
        event_name   text NOT NULL,
        payload_json jsonb NOT NULL,
        created_at   timestamptz NOT NULL DEFAULT now(),
        PRIMARY KEY (workflow_name_shard, run_id, event_name),
        FOREIGN KEY (workflow_name_shard, run_id)
                REFERENCES "flows"."runs"(workflow_name_shard, run_id)
                ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "flows"."random" (
        workflow_name_shard text NOT NULL,
        run_id              uuid NOT NULL,
        rand_key     text NOT NULL,
        kind         text NOT NULL,
        value_text   text,
        value_bigint bigint,
        created_at   timestamptz NOT NULL DEFAULT now(),
        PRIMARY KEY (workflow_name_shard, run_id, rand_key),
        FOREIGN KEY (workflow_name_shard, run_id)
                REFERENCES "flows"."runs"(workflow_name_shard, run_id)
                ON DELETE CASCADE
);
