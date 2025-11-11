package flows

import (
	"fmt"
	"hash/fnv"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// ShardConfig defines the sharding configuration for workflow names.
type ShardConfig struct {
	NumShards int // Number of shards (default: 3)
}

// DefaultShardConfig returns the default sharding configuration with 3 shards.
func DefaultShardConfig() *ShardConfig {
	return &ShardConfig{
		NumShards: 3,
	}
}

var (
	globalShardConfig     = DefaultShardConfig()
	globalShardConfigLock sync.RWMutex
)

// SetShardConfig sets the global shard configuration.
// Must be called before any workflows are started.
func SetShardConfig(config *ShardConfig) {
	globalShardConfigLock.Lock()
	defer globalShardConfigLock.Unlock()
	globalShardConfig = config
}

// GetShardConfig returns the current global shard configuration.
func GetShardConfig() *ShardConfig {
	globalShardConfigLock.RLock()
	defer globalShardConfigLock.RUnlock()
	return globalShardConfig
}

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

// Global consistent hash ring (100 replicas for good distribution)
var (
	globalHashRing     *ConsistentHash
	globalHashRingOnce sync.Once
)

// initHashRing initializes the global hash ring with the configured number of shards.
func initHashRing() {
	globalHashRingOnce.Do(func() {
		config := GetShardConfig()
		globalHashRing = NewConsistentHash(100) // 100 virtual nodes per shard
		for i := 0; i < config.NumShards; i++ {
			globalHashRing.Add(i)
		}
	})
}

// GetShardForWorkflow returns the shard number for a given workflow ID using consistent hashing.
// This provides stable shard assignment that only changes for ~1/N workflows when adding a new shard.
func GetShardForWorkflow(workflowID uuid.UUID) int {
	initHashRing()
	return globalHashRing.Get(workflowID.String())
}

// GetShardedWorkflowName returns the workflow name with shard suffix.
// Format: "{baseName}-shard-{shardNum}"
// Example: "loan-application-workflow" -> "loan-application-workflow-shard-0"
func GetShardedWorkflowName(baseName string, workflowID uuid.UUID) string {
	shard := GetShardForWorkflow(workflowID)
	return fmt.Sprintf("%s-shard-%d", baseName, shard)
}

// GetAllShardedWorkflowNames returns all possible sharded names for a workflow type.
// Workers should poll all shards for their workflow type.
// Example: "loan-application-workflow" -> ["...-shard-0", "...-shard-1", "...-shard-2"]
func GetAllShardedWorkflowNames(baseName string) []string {
	config := GetShardConfig()
	names := make([]string, config.NumShards)
	for i := 0; i < config.NumShards; i++ {
		names[i] = fmt.Sprintf("%s-shard-%d", baseName, i)
	}
	return names
}

// ExtractBaseWorkflowName extracts the base workflow name from a sharded name.
// Example: "loan-application-workflow-shard-0" -> "loan-application-workflow"
func ExtractBaseWorkflowName(shardedName string) string {
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
