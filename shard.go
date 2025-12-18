package flows

import (
	"fmt"
	"hash/fnv"
)

// workflowNameShard computes the shard value for a workflow run.
//
// On Citus, the runs table is distributed by workflow_name_shard. This function
// deterministically assigns each run to a shard based on its runID, producing
// a value like "my_workflow_3" for workflow "my_workflow" with shard index 3.
//
// The shard index is computed as: hash(runID) % shardCount
func workflowNameShard(workflowName string, runID RunID, shardCount int) string {
	if shardCount <= 0 {
		shardCount = 1
	}
	if shardCount == 1 {
		return fmt.Sprintf("%s_%d", workflowName, 0)
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	shard := int(h.Sum32() % uint32(shardCount))
	return fmt.Sprintf("%s_%d", workflowName, shard)
}

// ShardValuesForWorkflow returns all possible shard values for a workflow name.
//
// On Citus, SELECT ... FOR UPDATE SKIP LOCKED must be routed to a single shard,
// which requires an equality predicate on the distribution column (workflow_name_shard).
// The worker iterates through all shard values returned by this function to find work.
//
// Example: ShardValuesForWorkflow("my_workflow", 4) returns
// ["my_workflow_0", "my_workflow_1", "my_workflow_2", "my_workflow_3"]
func ShardValuesForWorkflow(workflowName string, shardCount int) []string {
	if shardCount <= 0 {
		shardCount = 1
	}
	shards := make([]string, shardCount)
	for i := 0; i < shardCount; i++ {
		shards[i] = fmt.Sprintf("%s_%d", workflowName, i)
	}
	return shards
}
