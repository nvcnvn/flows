package flows

import (
	"testing"

	"github.com/google/uuid"
)

func TestConsistentHashDistribution(t *testing.T) {
	// Test that consistent hash distributes keys across shards
	// Note: Consistent hashing prioritizes stability over perfect balance
	numShards := 3
	numKeys := 10000

	ch := NewConsistentHash(100) // 100 replicas per shard
	for i := 0; i < numShards; i++ {
		ch.Add(i)
	}

	// Count distribution
	shardCounts := make(map[int]int)
	for i := 0; i < numKeys; i++ {
		key := uuid.New().String()
		shard := ch.Get(key)
		shardCounts[shard]++
	}

	// Verify all shards got some keys (at least 5% of total)
	minKeysPerShard := numKeys / 20 // 5% minimum
	if len(shardCounts) != numShards {
		t.Errorf("Expected %d shards to receive keys, got %d", numShards, len(shardCounts))
	}

	for shard := 0; shard < numShards; shard++ {
		count := shardCounts[shard]
		if count < minKeysPerShard {
			t.Errorf("Shard %d has too few keys: %d (expected at least %d)",
				shard, count, minKeysPerShard)
		}
	}

	// Verify total distribution equals total keys
	total := 0
	for _, count := range shardCounts {
		total += count
	}
	if total != numKeys {
		t.Errorf("Total distributed keys %d != expected %d", total, numKeys)
	}

	t.Logf("Distribution across %d shards: %v", numShards, shardCounts)
	for shard, count := range shardCounts {
		percentage := float64(count) / float64(numKeys) * 100
		t.Logf("  Shard %d: %.1f%%", shard, percentage)
	}
}

func TestConsistentHashStability(t *testing.T) {
	// Test that the same key always maps to the same shard
	numShards := 3
	ch := NewConsistentHash(100)
	for i := 0; i < numShards; i++ {
		ch.Add(i)
	}

	// Generate test keys and their shard assignments
	testKeys := make([]string, 100)
	expectedShards := make([]int, 100)
	for i := 0; i < 100; i++ {
		testKeys[i] = uuid.New().String()
		expectedShards[i] = ch.Get(testKeys[i])
	}

	// Verify keys map to same shard on repeated lookups
	for i := 0; i < 100; i++ {
		shard := ch.Get(testKeys[i])
		if shard != expectedShards[i] {
			t.Errorf("Key %s mapped to shard %d on first lookup, but shard %d on second lookup",
				testKeys[i], expectedShards[i], shard)
		}
	}
}

func TestConsistentHashMinimalReassignment(t *testing.T) {
	// Test that adding a shard only reassigns ~1/N keys
	initialShards := 3
	numKeys := 10000

	// Create initial hash ring with 3 shards
	ch := NewConsistentHash(100)
	for i := 0; i < initialShards; i++ {
		ch.Add(i)
	}

	// Map keys to initial shards
	testKeys := make([]string, numKeys)
	initialMapping := make(map[string]int)
	for i := 0; i < numKeys; i++ {
		testKeys[i] = uuid.New().String()
		initialMapping[testKeys[i]] = ch.Get(testKeys[i])
	}

	// Add a new shard
	ch.Add(initialShards)

	// Count how many keys were reassigned
	reassigned := 0
	for _, key := range testKeys {
		newShard := ch.Get(key)
		if newShard != initialMapping[key] {
			reassigned++
		}
	}

	// With consistent hashing, ~1/(N+1) keys should be reassigned when adding 1 shard
	// For 3 -> 4 shards, expect ~25% reassignment, but allow wider tolerance
	// since actual distribution depends on hash function and virtual nodes

	// Allow 10-40% reassignment range (consistent hashing may vary)
	minReassignment := float64(numKeys) * 0.10
	maxReassignment := float64(numKeys) * 0.40

	if float64(reassigned) < minReassignment || float64(reassigned) > maxReassignment {
		t.Errorf("Expected 10-40%% keys to be reassigned when adding shard, got %d (%.1f%%)",
			reassigned, float64(reassigned)/float64(numKeys)*100)
	}

	t.Logf("Adding shard %d: %d/%d keys reassigned (%.1f%%)",
		initialShards, reassigned, numKeys,
		float64(reassigned)/float64(numKeys)*100)
}

func TestGetShardForWorkflow(t *testing.T) {
	t.Parallel()

	// Create test hash ring with 3 shards
	hashRing := NewConsistentHash(100)
	for i := 0; i < 3; i++ {
		hashRing.Add(i)
	}

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
		shard := getShardForWorkflow(wfID, hashRing)
		if shard < 0 || shard >= 3 {
			t.Errorf("Shard %d out of range [0, 3) for workflow %s", shard, wfID)
		}
		shardAssignments[wfID] = shard
	}

	// Verify consistency - same workflow ID should always get same shard
	for i := 0; i < 10; i++ {
		for wfID, expectedShard := range shardAssignments {
			shard := getShardForWorkflow(wfID, hashRing)
			if shard != expectedShard {
				t.Errorf("Workflow %s got shard %d, expected %d (iteration %d)",
					wfID, shard, expectedShard, i)
			}
		}
	}
}

func TestGetShardedWorkflowName(t *testing.T) {
	t.Parallel()

	// Create test hash ring with 3 shards
	hashRing := NewConsistentHash(100)
	for i := 0; i < 3; i++ {
		hashRing.Add(i)
	}

	baseName := "test-workflow"
	workflowID := uuid.New()

	shardedName := getShardedWorkflowName(baseName, workflowID, hashRing)

	// Verify format
	expectedShard := getShardForWorkflow(workflowID, hashRing)
	expectedName := "test-workflow-shard-" + string(rune('0'+expectedShard))

	if shardedName != expectedName {
		t.Errorf("Expected sharded name %s, got %s", expectedName, shardedName)
	}

	// Verify consistency
	for i := 0; i < 10; i++ {
		name := getShardedWorkflowName(baseName, workflowID, hashRing)
		if name != shardedName {
			t.Errorf("Sharded name changed from %s to %s on iteration %d",
				shardedName, name, i)
		}
	}
}

func TestGetAllShardedWorkflowNames(t *testing.T) {
	t.Parallel()

	// Create test hash ring with 5 shards
	hashRing := NewConsistentHash(100)
	for i := 0; i < 5; i++ {
		hashRing.Add(i)
	}

	baseName := "my-workflow"
	names := getAllShardedWorkflowNames(baseName, hashRing)

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

func TestHashRingThreadSafety(t *testing.T) {
	t.Parallel()

	// Test concurrent reads and writes to hash ring
	hashRing := NewConsistentHash(100)
	for i := 0; i < 3; i++ {
		hashRing.Add(i)
	}

	done := make(chan bool)

	// Multiple readers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				workflowID := uuid.New()
				shard := getShardForWorkflow(workflowID, hashRing)
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
				key := uuid.New().String()
				hashRing.Get(key)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}
}

func TestConsistentHashEmptyRing(t *testing.T) {
	t.Parallel()

	ch := NewConsistentHash(100)

	// Getting from empty ring should return 0
	shard := ch.Get("test-key")
	if shard != 0 {
		t.Errorf("Expected shard 0 for empty ring, got %d", shard)
	}
}

func BenchmarkConsistentHashGet(b *testing.B) {
	ch := NewConsistentHash(100)
	for i := 0; i < 10; i++ {
		ch.Add(i)
	}

	keys := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		keys[i] = uuid.New().String()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch.Get(keys[i%1000])
	}
}

func BenchmarkGetShardForWorkflow(b *testing.B) {
	hashRing := NewConsistentHash(100)
	for i := 0; i < 10; i++ {
		hashRing.Add(i)
	}

	workflowIDs := make([]uuid.UUID, 1000)
	for i := 0; i < 1000; i++ {
		workflowIDs[i] = uuid.New()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getShardForWorkflow(workflowIDs[i%1000], hashRing)
	}
}
