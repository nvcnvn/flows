package flows

import (
	"unicode"

	"github.com/jackc/pgx/v5"
)

// DefaultSchema is the schema used by this package when none is configured.
//
// With unprefixed table names (runs, steps, waits, events, random), using a
// dedicated schema avoids collisions with application tables.
const DefaultSchema = "flows"

// DBConfig configures where Flows stores its tables.
type DBConfig struct {
	// Schema is the Postgres schema containing the flows tables.
	// If empty, DefaultSchema is used.
	Schema string

	// ShardCount controls how many shards a workflow can spread across.
	//
	// When using Citus (or any sharded Postgres setup), Flows routes writes/updates
	// using a distribution column `workflow_name_shard`, derived as
	// `workflow_name_<n>` where n is in [0, ShardCount).
	//
	// If ShardCount is <= 0, it defaults to 1.
	ShardCount int
}

func (c DBConfig) schema() string {
	if c.Schema == "" {
		return DefaultSchema
	}
	// Keep identifiers conservative to avoid SQL injection. If invalid, fall back.
	for i, r := range c.Schema {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return DefaultSchema
		}
		if i == 0 && unicode.IsDigit(r) {
			return DefaultSchema
		}
	}
	return c.Schema
}

func (c DBConfig) shardCount() int {
	if c.ShardCount <= 0 {
		return 1
	}
	return c.ShardCount
}

type dbTables struct {
	runs   string
	steps  string
	waits  string
	events string
	random string
}

func newDBTables(cfg DBConfig) dbTables {
	schema := cfg.schema()
	return dbTables{
		runs:   pgx.Identifier{schema, "runs"}.Sanitize(),
		steps:  pgx.Identifier{schema, "steps"}.Sanitize(),
		waits:  pgx.Identifier{schema, "waits"}.Sanitize(),
		events: pgx.Identifier{schema, "events"}.Sanitize(),
		random: pgx.Identifier{schema, "random"}.Sanitize(),
	}
}
