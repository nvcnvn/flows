-- Distribute the runs table by workflow_name_shard
SELECT create_distributed_table('flows.runs', 'workflow_name_shard');

-- Distribute child tables colocated with runs for foreign key support
SELECT create_distributed_table('flows.steps', 'workflow_name_shard', colocate_with => 'flows.runs');
SELECT create_distributed_table('flows.waits', 'workflow_name_shard', colocate_with => 'flows.runs');
SELECT create_distributed_table('flows.events', 'workflow_name_shard', colocate_with => 'flows.runs');
SELECT create_distributed_table('flows.random', 'workflow_name_shard', colocate_with => 'flows.runs');

-- Schedules is a reference table (replicated to all nodes) because it is
-- not sharded by workflow_name_shard and is small (one row per cron schedule).
SELECT create_reference_table('flows.schedules');
