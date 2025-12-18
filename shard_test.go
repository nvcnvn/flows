package flows

import (
	"fmt"
	"testing"
	"time"
)

func testGenerateUUID() string {
	uuid, _ := newUUIDv7(time.Now())
	return uuid
}

func TestWorkflowNameShard(t *testing.T) {
	tests := []struct {
		name         string
		workflowName string
		runID        RunID
		shardCount   int
		wantPrefix   string
		wantShardIdx int // -1 means any valid shard
	}{
		{
			name:         "single shard always returns 0",
			workflowName: "my_workflow",
			runID:        "run-123",
			shardCount:   1,
			wantPrefix:   "my_workflow_",
			wantShardIdx: 0,
		},
		{
			name:         "zero shard count treated as 1",
			workflowName: "my_workflow",
			runID:        "run-123",
			shardCount:   0,
			wantPrefix:   "my_workflow_",
			wantShardIdx: 0,
		},
		{
			name:         "negative shard count treated as 1",
			workflowName: "my_workflow",
			runID:        "run-123",
			shardCount:   -5,
			wantPrefix:   "my_workflow_",
			wantShardIdx: 0,
		},
		{
			name:         "multiple shards produces valid shard",
			workflowName: "order_workflow",
			runID:        "run-456",
			shardCount:   8,
			wantPrefix:   "order_workflow_",
			wantShardIdx: -1, // any valid shard 0-7
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowNameShard(tt.workflowName, tt.runID, tt.shardCount)

			// Check prefix
			if len(got) < len(tt.wantPrefix) || got[:len(tt.wantPrefix)] != tt.wantPrefix {
				t.Errorf("workflowNameShard() = %q, want prefix %q", got, tt.wantPrefix)
			}

			// Parse shard index
			var shardIdx int
			if _, err := fmt.Sscanf(got, tt.workflowName+"_%d", &shardIdx); err != nil {
				t.Errorf("failed to parse shard index from %q: %v", got, err)
				return
			}

			if tt.wantShardIdx >= 0 && shardIdx != tt.wantShardIdx {
				t.Errorf("workflowNameShard() shard index = %d, want %d", shardIdx, tt.wantShardIdx)
			}

			effectiveShardCount := tt.shardCount
			if effectiveShardCount <= 0 {
				effectiveShardCount = 1
			}
			if shardIdx < 0 || shardIdx >= effectiveShardCount {
				t.Errorf("workflowNameShard() shard index = %d, out of range [0, %d)", shardIdx, effectiveShardCount)
			}
		})
	}
}

func TestWorkflowNameShardDeterministic(t *testing.T) {
	// Same inputs should always produce the same shard
	for i := 0; i < 100; i++ {
		shard1 := workflowNameShard("test_workflow", "run-abc-123", 16)
		shard2 := workflowNameShard("test_workflow", "run-abc-123", 16)
		if shard1 != shard2 {
			t.Fatalf("workflowNameShard is not deterministic: %q != %q", shard1, shard2)
		}
	}
}

func TestWorkflowNameShardDistribution(t *testing.T) {
	// Test that different run IDs distribute across shards
	shardCount := 8
	counts := make(map[string]int)

	// Generate many run IDs and count shard distribution
	for i := 0; i < 1000; i++ {
		runID := RunID(testGenerateUUID()) // Use real UUIDs for realistic distribution
		shard := workflowNameShard("test_workflow", runID, shardCount)
		counts[shard]++
	}

	// Verify all shards are used
	if len(counts) != shardCount {
		t.Errorf("expected %d unique shards, got %d", shardCount, len(counts))
	}

	// Verify reasonable distribution (each shard should have at least 50 runs out of 1000)
	for shard, count := range counts {
		if count < 50 {
			t.Errorf("shard %q has only %d runs, expected more even distribution", shard, count)
		}
	}
}

func TestShardValuesForWorkflow(t *testing.T) {
	tests := []struct {
		name         string
		workflowName string
		shardCount   int
		want         []string
	}{
		{
			name:         "single shard",
			workflowName: "my_workflow",
			shardCount:   1,
			want:         []string{"my_workflow_0"},
		},
		{
			name:         "zero shard count treated as 1",
			workflowName: "my_workflow",
			shardCount:   0,
			want:         []string{"my_workflow_0"},
		},
		{
			name:         "negative shard count treated as 1",
			workflowName: "my_workflow",
			shardCount:   -5,
			want:         []string{"my_workflow_0"},
		},
		{
			name:         "multiple shards",
			workflowName: "order_workflow",
			shardCount:   4,
			want:         []string{"order_workflow_0", "order_workflow_1", "order_workflow_2", "order_workflow_3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShardValuesForWorkflow(tt.workflowName, tt.shardCount)
			if len(got) != len(tt.want) {
				t.Errorf("ShardValuesForWorkflow() = %v, want %v", got, tt.want)
				return
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("ShardValuesForWorkflow()[%d] = %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}

func TestShardValuesContainsAllPossibleShards(t *testing.T) {
	// Verify that any shard produced by workflowNameShard is in ShardValuesForWorkflow
	shardCount := 32
	workflowName := "test_workflow"
	validShards := make(map[string]bool)
	for _, s := range ShardValuesForWorkflow(workflowName, shardCount) {
		validShards[s] = true
	}

	// Generate many run IDs and verify their shards are in the valid set
	for i := 0; i < 1000; i++ {
		runID := RunID(testGenerateUUID())
		shard := workflowNameShard(workflowName, runID, shardCount)
		if !validShards[shard] {
			t.Errorf("workflowNameShard() = %q is not in ShardValuesForWorkflow()", shard)
		}
	}
}
