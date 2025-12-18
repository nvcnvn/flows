package flows

import (
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SchemaSQL is the default schema (DefaultSchema) required by this package.
//
// Notes:
// - `run_id` is stored as Postgres `uuid`. UUIDv7 generation is done in Go.
// - payloads are stored as jsonb (default codec is JSON).
var SchemaSQL = SchemaSQLFor(DefaultSchema)

// SchemaSQLFor returns the schema required by this package for a given Postgres schema name.
//
// The schema name is validated conservatively and will fall back to DefaultSchema if invalid.
func SchemaSQLFor(schema string) string {
	cfg := DBConfig{Schema: schema}
	schema = cfg.schema()
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	t := newDBTables(cfg)

	return fmt.Sprintf(`
CREATE SCHEMA IF NOT EXISTS %s;

CREATE TABLE IF NOT EXISTS %s (
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
	ON %s (workflow_name_shard, status, next_wake_at, created_at);

CREATE TABLE IF NOT EXISTS %s (
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
		REFERENCES %s(workflow_name_shard, run_id)
		ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS %s (
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
		REFERENCES %s(workflow_name_shard, run_id)
		ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS waits_event_idx
	ON %s (workflow_name_shard, event_name, satisfied_at);

-- One event per (run,event_name). If you need multiple events, add an event_id and remove this PK.
CREATE TABLE IF NOT EXISTS %s (
	workflow_name_shard text NOT NULL,
	run_id              uuid NOT NULL,
	event_name   text NOT NULL,
	payload_json jsonb NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (workflow_name_shard, run_id, event_name),
	FOREIGN KEY (workflow_name_shard, run_id)
		REFERENCES %s(workflow_name_shard, run_id)
		ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS %s (
	workflow_name_shard text NOT NULL,
	run_id              uuid NOT NULL,
	rand_key     text NOT NULL,
	kind         text NOT NULL,
	value_text   text,
	value_bigint bigint,
	created_at   timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (workflow_name_shard, run_id, rand_key),
	FOREIGN KEY (workflow_name_shard, run_id)
		REFERENCES %s(workflow_name_shard, run_id)
		ON DELETE CASCADE
);
`,
		schemaIdent,
		t.runs,
		t.runs,
		t.steps,
		t.runs,
		t.waits,
		t.runs,
		t.waits,
		t.events,
		t.runs,
		t.random,
		t.runs,
	)
}

// CitusSchemaSQLFor returns the Citus distributed table setup SQL for a given Postgres schema name.
//
// This should be run AFTER SchemaSQL has created the tables. It distributes all tables
// by the `workflow_name_shard` column and colocates child tables with the runs table.
//
// The schema name is validated conservatively and will fall back to DefaultSchema if invalid.
func CitusSchemaSQLFor(schema string) string {
	cfg := DBConfig{Schema: schema}
	schema = cfg.schema()

	// Citus requires tables to be distributed after creation.
	// We distribute runs first, then colocate child tables with it.
	// Foreign keys only work between colocated distributed tables in Citus.
	//
	// The table names are passed as string literals to create_distributed_table.
	runs := schema + ".runs"
	steps := schema + ".steps"
	waits := schema + ".waits"
	events := schema + ".events"
	random := schema + ".random"

	return fmt.Sprintf(`
-- Distribute the runs table by workflow_name_shard
SELECT create_distributed_table('%s', 'workflow_name_shard');

-- Distribute child tables colocated with runs for foreign key support
SELECT create_distributed_table('%s', 'workflow_name_shard', colocate_with => '%s');
SELECT create_distributed_table('%s', 'workflow_name_shard', colocate_with => '%s');
SELECT create_distributed_table('%s', 'workflow_name_shard', colocate_with => '%s');
SELECT create_distributed_table('%s', 'workflow_name_shard', colocate_with => '%s');
`,
		runs,
		steps, runs,
		waits, runs,
		events, runs,
		random, runs,
	)
}

// CitusSchemaSQL is the Citus distributed table setup for the default schema (DefaultSchema).
var CitusSchemaSQL = CitusSchemaSQLFor(DefaultSchema)
