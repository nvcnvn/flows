package flows

import (
	"fmt"
	"hash/fnv"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// DefaultNumShards is the default number of shards for workflow distribution.
const DefaultNumShards = 3

// ConsistentHash implements consistent hashing for distributed workflow placement.
// Uses virtual nodes (replicas) to ensure balanced distribution when adding/removing shards.
type ConsistentHash struct {
	replicas int         // Number of virtual nodes per shard
	keys     []int       // Sorted hash ring
	hashMap  map[int]int // Hash to shard mapping
	mu       sync.RWMutex
}

// NewConsistentHash creates a new consistent hash ring with the given number of replicas.
// Higher replicas provide better distribution but use more memory.
func NewConsistentHash(replicas int) *ConsistentHash {
	return &ConsistentHash{
		replicas: replicas,
		hashMap:  make(map[int]int),
	}
}

// Add adds a shard to the hash ring.
func (h *ConsistentHash) Add(shard int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := 0; i < h.replicas; i++ {
		key := h.hash(fmt.Sprintf("%d-%d", shard, i))
		h.keys = append(h.keys, key)
		h.hashMap[key] = shard
	}
	sort.Ints(h.keys)
}

// Get returns the shard for a given key using consistent hashing.
func (h *ConsistentHash) Get(key string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.keys) == 0 {
		return 0
	}

	hash := h.hash(key)

	// Binary search for the first node >= hash
	idx := sort.Search(len(h.keys), func(i int) bool {
		return h.keys[i] >= hash
	})

	// Wrap around if we're past the end
	if idx == len(h.keys) {
		idx = 0
	}

	return h.hashMap[h.keys[idx]]
}

// hash computes FNV-1a hash for a string.
func (h *ConsistentHash) hash(key string) int {
	hasher := fnv.New32a()
	hasher.Write([]byte(key))
	return int(hasher.Sum32())
}

// NumShards returns the number of shards in this hash ring.
func (h *ConsistentHash) NumShards() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Count unique shards
	shards := make(map[int]bool)
	for _, shard := range h.hashMap {
		shards[shard] = true
	}
	return len(shards)
}

// NewDefaultHashRing creates a hash ring with the default configuration (3 shards, 100 replicas).
func NewDefaultHashRing() *ConsistentHash {
	h := NewConsistentHash(100) // 100 virtual nodes per shard
	for i := 0; i < DefaultNumShards; i++ {
		h.Add(i)
	}
	return h
}

// Global consistent hash ring
var (
	globalHashRing   *ConsistentHash
	globalHashRingMu sync.RWMutex
)

// SetHashRing sets the global hash ring instance for use with package-level functions.
// This is optional - you can also pass custom hash rings to Engine/Worker.
//
// Example with global hash ring:
//
//	hashRing := flows.NewDefaultHashRing()
//	flows.SetHashRing(hashRing)
//	exec, err := flows.Start(ctx, myWorkflow, input)
//
// Example with custom shards:
//
//	hashRing := flows.NewConsistentHash(100)
//	for i := 0; i < 5; i++ {
//		hashRing.Add(i)
//	}
//	flows.SetHashRing(hashRing)
func SetHashRing(hashRing *ConsistentHash) {
	globalHashRingMu.Lock()
	defer globalHashRingMu.Unlock()
	globalHashRing = hashRing
}

// getGlobalHashRing safely retrieves the global hash ring instance.
// If not set, returns a default hash ring with 3 shards.
func getGlobalHashRing() *ConsistentHash {
	globalHashRingMu.RLock()
	defer globalHashRingMu.RUnlock()

	if globalHashRing == nil {
		// Return default hash ring if not set
		globalHashRingMu.RUnlock()
		globalHashRingMu.Lock()
		if globalHashRing == nil {
			globalHashRing = NewDefaultHashRing()
		}
		globalHashRingMu.Unlock()
		globalHashRingMu.RLock()
	}

	return globalHashRing
}

// getShardForWorkflow returns the shard number for a given workflow ID using consistent hashing.
// If hashRing is nil, uses the global hash ring.
func getShardForWorkflow(workflowID uuid.UUID, hashRing *ConsistentHash) int {
	if hashRing == nil {
		hashRing = getGlobalHashRing()
	}
	return hashRing.Get(workflowID.String())
}

// getShardedWorkflowName returns the workflow name with shard suffix.
// Format: "{baseName}-shard-{shardNum}"
// Example: "loan-application-workflow" -> "loan-application-workflow-shard-0"
// If hashRing is nil, uses the global hash ring.
func getShardedWorkflowName(baseName string, workflowID uuid.UUID, hashRing *ConsistentHash) string {
	shard := getShardForWorkflow(workflowID, hashRing)
	return fmt.Sprintf("%s-shard-%d", baseName, shard)
}

// getAllShardedWorkflowNames returns all possible sharded names for a workflow type.
// Workers should poll all shards for their workflow type.
// Example: "loan-application-workflow" -> ["...-shard-0", "...-shard-1", "...-shard-2"]
// If hashRing is nil, uses the global hash ring.
func getAllShardedWorkflowNames(baseName string, hashRing *ConsistentHash) []string {
	if hashRing == nil {
		hashRing = getGlobalHashRing()
	}
	numShards := hashRing.NumShards()
	names := make([]string, numShards)
	for i := 0; i < numShards; i++ {
		names[i] = fmt.Sprintf("%s-shard-%d", baseName, i)
	}
	return names
}

// extractBaseWorkflowName extracts the base workflow name from a sharded name.
// Example: "loan-application-workflow-shard-0" -> "loan-application-workflow"
func extractBaseWorkflowName(shardedName string) string {
	// Simple approach: find last "-shard-" and strip it
	const suffix = "-shard-"
	for i := len(shardedName) - 1; i >= 0; i-- {
		if i+len(suffix) <= len(shardedName) && shardedName[i:i+len(suffix)] == suffix {
			return shardedName[:i]
		}
	}
	// If no shard suffix found, return as-is (might be unsharded)
	return shardedName
}
