-- if you odn't use citus, you can just skip this migration
CREATE EXTENSION IF NOT EXISTS citus;

-- Distribute workflows table by workflow name with co-location
SELECT create_distributed_table('workflows', 'name');

SELECT create_distributed_table('activities', 'workflow_name', colocate_with => 'workflows');

SELECT create_distributed_table('history_events', 'workflow_name', colocate_with => 'workflows');

SELECT create_distributed_table('timers', 'workflow_name', colocate_with => 'workflows');

SELECT create_distributed_table('signals', 'workflow_name', colocate_with => 'workflows');

SELECT create_distributed_table('task_queue', 'workflow_name', colocate_with => 'workflows');

SELECT create_distributed_table('dead_letter_queue', 'workflow_name', colocate_with => 'workflows');
