package flows

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestShardStability(t *testing.T) {
	// Test that the same workflow ID always maps to the same shard
	numShards := 9
	config := NewShardConfig(numShards)

	// Generate test workflow IDs and their shard assignments
	testIDs := make([]uuid.UUID, 100)
	expectedShards := make([]int, 100)
	for i := 0; i < 100; i++ {
		testIDs[i] = uuid.New()
		expectedShards[i] = config.GetShard(testIDs[i])
	}

	// Verify workflow IDs map to same shard on repeated lookups
	for i := 0; i < 100; i++ {
		shard := config.GetShard(testIDs[i])
		if shard != expectedShards[i] {
			t.Errorf("Workflow ID %s mapped to shard %d on first lookup, but shard %d on second lookup",
				testIDs[i], expectedShards[i], shard)
		}
	}
}

func TestGetShardForWorkflow(t *testing.T) {
	t.Parallel()

	// Create test shard config with 9 shards
	shardConfig := NewShardConfig(9)

	// Test that workflow IDs consistently map to shards
	testWorkflows := []uuid.UUID{
		uuid.New(),
		uuid.New(),
		uuid.New(),
		uuid.New(),
		uuid.New(),
	}

	// Get initial shard assignments
	shardAssignments := make(map[uuid.UUID]int)
	for _, wfID := range testWorkflows {
		shard := getShardForWorkflow(wfID, shardConfig)
		if shard < 0 || shard >= 9 {
			t.Errorf("Shard %d out of range [0, 9) for workflow %s", shard, wfID)
		}
		shardAssignments[wfID] = shard
	}

	// Verify consistency - same workflow ID should always get same shard
	for i := 0; i < 10; i++ {
		for wfID, expectedShard := range shardAssignments {
			shard := getShardForWorkflow(wfID, shardConfig)
			if shard != expectedShard {
				t.Errorf("Workflow %s got shard %d, expected %d (iteration %d)",
					wfID, shard, expectedShard, i)
			}
		}
	}
}

func TestGetShardedWorkflowName(t *testing.T) {
	t.Parallel()

	// Create test shard config with 9 shards
	shardConfig := NewShardConfig(9)

	baseName := "test-workflow"
	workflowID := uuid.New()

	shardedName := getShardedWorkflowName(baseName, workflowID, shardConfig)

	// Verify format
	expectedShard := getShardForWorkflow(workflowID, shardConfig)
	expectedName := fmt.Sprintf("test-workflow-shard-%d", expectedShard)

	if shardedName != expectedName {
		t.Errorf("Expected sharded name %s, got %s", expectedName, shardedName)
	}

	// Verify consistency
	for i := 0; i < 10; i++ {
		name := getShardedWorkflowName(baseName, workflowID, shardConfig)
		if name != shardedName {
			t.Errorf("Sharded name changed from %s to %s on iteration %d",
				shardedName, name, i)
		}
	}
}

func TestGetAllShardedWorkflowNames(t *testing.T) {
	t.Parallel()

	// Create test shard config with 5 shards
	shardConfig := NewShardConfig(5)

	baseName := "my-workflow"
	names := getAllShardedWorkflowNames(baseName, shardConfig)

	if len(names) != 5 {
		t.Errorf("Expected 5 sharded names, got %d", len(names))
	}

	expectedNames := []string{
		"my-workflow-shard-0",
		"my-workflow-shard-1",
		"my-workflow-shard-2",
		"my-workflow-shard-3",
		"my-workflow-shard-4",
	}

	for i, expected := range expectedNames {
		if names[i] != expected {
			t.Errorf("Expected names[%d] = %s, got %s", i, expected, names[i])
		}
	}
}

func TestExtractBaseWorkflowName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shardedName  string
		expectedBase string
	}{
		{"loan-application-shard-0", "loan-application"},
		{"loan-application-shard-1", "loan-application"},
		{"my-workflow-shard-2", "my-workflow"},
		{"simple-shard-0", "simple"},
		{"nosuffix", "nosuffix"},           // No shard suffix at all
		{"workflow-name", "workflow-name"}, // No shard suffix
		{"complex-workflow-name-shard-3", "complex-workflow-name"},
		{"has-shard-in-name-shard-1", "has-shard-in-name"}, // Has "shard" in name but also has suffix
	}

	for _, tc := range tests {
		baseName := extractBaseWorkflowName(tc.shardedName)
		if baseName != tc.expectedBase {
			t.Errorf("extractBaseWorkflowName(%s) = %s, expected %s",
				tc.shardedName, baseName, tc.expectedBase)
		}
	}
}

func TestShardConfigThreadSafety(t *testing.T) {
	t.Parallel()

	// Test concurrent reads to shard config
	shardConfig := NewShardConfig(9)

	done := make(chan bool)

	// Multiple readers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				workflowID := uuid.New()
				shard := getShardForWorkflow(workflowID, shardConfig)
				if shard < 0 {
					t.Errorf("getShardForWorkflow returned negative shard")
				}
			}
			done <- true
		}()
	}

	// Multiple concurrent gets
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				workflowID := uuid.New()
				shardConfig.GetShard(workflowID)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}
}

func BenchmarkShardConfigGetShard(b *testing.B) {
	config := NewShardConfig(9)

	workflowIDs := make([]uuid.UUID, 1000)
	for i := 0; i < 1000; i++ {
		workflowIDs[i] = uuid.New()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config.GetShard(workflowIDs[i%1000])
	}
}

func BenchmarkGetShardForWorkflow(b *testing.B) {
	shardConfig := NewShardConfig(9)

	workflowIDs := make([]uuid.UUID, 1000)
	for i := 0; i < 1000; i++ {
		workflowIDs[i] = uuid.New()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getShardForWorkflow(workflowIDs[i%1000], shardConfig)
	}
}
