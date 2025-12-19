package flows

// This file centralizes SQL statement strings so call sites don't need to
// format table names inline. The only dynamic part is the schema-qualified
// table name embedded in dbTables.

func (t dbTables) insertRunSQL() string {
	return `INSERT INTO ` + t.runs + ` (workflow_name_shard, run_id, workflow_name, status, input_json, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())`
}

func (t dbTables) upsertEventSQL() string {
	return `
		INSERT INTO ` + t.events + ` (workflow_name_shard, run_id, event_name, payload_json)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (workflow_name_shard, run_id, event_name) DO UPDATE
		SET payload_json = EXCLUDED.payload_json
	`
}

func (t dbTables) satisfyEventWaitSQL() string {
	return `
		UPDATE ` + t.waits + `
		SET payload_json = $5,
			satisfied_at = now(),
			updated_at = now()
		WHERE workflow_name_shard = $1
			AND run_id = $2
			AND wait_type = $3
			AND event_name = $4
			AND satisfied_at IS NULL
	`
}

func (t dbTables) wakeRunFromEventSQL() string {
	return `UPDATE ` + t.runs + `
		SET status = $3, updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2 AND status = $4`
}

func (t dbTables) getRunStatusSQL() string {
	return `
		SELECT status, error_text, created_at, updated_at, next_wake_at
		FROM ` + t.runs + `
		WHERE workflow_name_shard = $1 AND run_id = $2
	`
}

func (t dbTables) cancelRunSQL() string {
	return `
		UPDATE ` + t.runs + `
		SET status = $3, error_text = 'cancelled by user', updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2
		AND status IN ($4, $5, $6)
	`
}

func (t dbTables) getRunStatusOnlySQL() string {
	return `SELECT status FROM ` + t.runs + ` WHERE workflow_name_shard = $1 AND run_id = $2`
}

func (t dbTables) getRunOutputSQL() string {
	return `
		SELECT status, output_json
		FROM ` + t.runs + `
		WHERE workflow_name_shard = $1 AND run_id = $2
	`
}

func (t dbTables) selectStepStatusSQL() string {
	return `SELECT status, output_json
		FROM ` + t.steps + `
		WHERE workflow_name_shard = $1 AND run_id = $2 AND step_key = $3`
}

func (t dbTables) upsertStepCompletedSQL() string {
	return `
		INSERT INTO ` + t.steps + ` (workflow_name_shard, run_id, step_key, status, input_json, output_json, attempts, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (workflow_name_shard, run_id, step_key) DO UPDATE
		SET status = EXCLUDED.status,
			input_json = EXCLUDED.input_json,
			output_json = EXCLUDED.output_json,
			attempts = EXCLUDED.attempts,
			updated_at = EXCLUDED.updated_at
	`
}

func (t dbTables) upsertStepFailedSQL() string {
	return `
		INSERT INTO ` + t.steps + ` (workflow_name_shard, run_id, step_key, status, error_text, attempts, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (workflow_name_shard, run_id, step_key) DO UPDATE
		SET status = EXCLUDED.status,
			error_text = EXCLUDED.error_text,
			attempts = EXCLUDED.attempts,
			updated_at = EXCLUDED.updated_at
	`
}

func (t dbTables) selectWaitStateSQL() string {
	return `SELECT wake_at, satisfied_at
		FROM ` + t.waits + `
		WHERE workflow_name_shard = $1 AND run_id = $2 AND wait_key = $3 AND wait_type = $4`
}

func (t dbTables) satisfySleepWaitSQL() string {
	return `UPDATE ` + t.waits + `
		SET satisfied_at = now(), updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2 AND wait_key = $3`
}

func (t dbTables) upsertSleepWaitSQL() string {
	return `
		INSERT INTO ` + t.waits + ` (workflow_name_shard, run_id, wait_key, wait_type, wake_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (workflow_name_shard, run_id, wait_key) DO UPDATE
		SET wake_at = EXCLUDED.wake_at,
			updated_at = EXCLUDED.updated_at
	`
}

func (t dbTables) setRunSleepingSQL() string {
	return `UPDATE ` + t.runs + `
		SET status = $3, next_wake_at = $4, updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2`
}

func (t dbTables) selectWaitPayloadSQL() string {
	return `SELECT payload_json, satisfied_at
		FROM ` + t.waits + `
		WHERE workflow_name_shard = $1 AND run_id = $2 AND wait_key = $3 AND wait_type = $4`
}

func (t dbTables) selectEventPayloadSQL() string {
	return `SELECT payload_json
		FROM ` + t.events + `
		WHERE workflow_name_shard = $1 AND run_id = $2 AND event_name = $3`
}

func (t dbTables) upsertSatisfiedEventWaitSQL() string {
	return `
		INSERT INTO ` + t.waits + ` (workflow_name_shard, run_id, wait_key, wait_type, event_name, payload_json, satisfied_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now())
		ON CONFLICT (workflow_name_shard, run_id, wait_key) DO UPDATE
		SET payload_json = EXCLUDED.payload_json,
			satisfied_at = EXCLUDED.satisfied_at,
			updated_at = EXCLUDED.updated_at
	`
}

func (t dbTables) upsertEventWaitSQL() string {
	return `
		INSERT INTO ` + t.waits + ` (workflow_name_shard, run_id, wait_key, wait_type, event_name, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (workflow_name_shard, run_id, wait_key) DO UPDATE
		SET event_name = EXCLUDED.event_name,
			updated_at = EXCLUDED.updated_at
	`
}

func (t dbTables) setRunWaitingEventSQL() string {
	return `UPDATE ` + t.runs + `
		SET status = $3, next_wake_at = NULL, updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2`
}

func (t dbTables) selectRandomUUIDv7SQL() string {
	return `SELECT value_text
		FROM ` + t.random + `
		WHERE workflow_name_shard = $1 AND run_id = $2 AND rand_key = $3 AND kind = 'uuidv7'`
}

func (t dbTables) upsertRandomUUIDv7SQL() string {
	return `
		INSERT INTO ` + t.random + ` (workflow_name_shard, run_id, rand_key, kind, value_text)
		VALUES ($1, $2, $3, 'uuidv7', $4)
		ON CONFLICT (workflow_name_shard, run_id, rand_key) DO UPDATE
		SET kind = EXCLUDED.kind,
			value_text = EXCLUDED.value_text
	`
}

func (t dbTables) selectRandomUint64SQL() string {
	return `SELECT value_bigint
		FROM ` + t.random + `
		WHERE workflow_name_shard = $1 AND run_id = $2 AND rand_key = $3 AND kind = 'uint64'`
}

func (t dbTables) upsertRandomUint64SQL() string {
	return `
		INSERT INTO ` + t.random + ` (workflow_name_shard, run_id, rand_key, kind, value_bigint)
		VALUES ($1, $2, $3, 'uint64', $4)
		ON CONFLICT (workflow_name_shard, run_id, rand_key) DO UPDATE
		SET kind = EXCLUDED.kind,
			value_bigint = EXCLUDED.value_bigint
	`
}

func (t dbTables) claimRunnableRunForShardSQL() string {
	return `
		SELECT run_id, input_json
		FROM ` + t.runs + `
		WHERE workflow_name_shard = $3
		  AND workflow_name = $4
		  AND (
		    status = $1
		    OR (status = $2 AND next_wake_at IS NOT NULL AND next_wake_at <= now())
		  )
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
}

func (t dbTables) setRunRunningSQL() string {
	return `UPDATE ` + t.runs + `
		SET status = $3, updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2`
}

func (t dbTables) setRunFailedSQL() string {
	return `UPDATE ` + t.runs + `
		SET status = $3, error_text = $4, updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2`
}

func (t dbTables) setRunCompletedSQL() string {
	return `UPDATE ` + t.runs + `
		SET status = $3, output_json = $4, error_text = NULL, next_wake_at = NULL, updated_at = now()
		WHERE workflow_name_shard = $1 AND run_id = $2`
}
