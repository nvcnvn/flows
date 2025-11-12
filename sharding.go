package flows

import (
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/google/uuid"
)

// DefaultNumShards is the default number of shards for workflow distribution.
// Using 9 shards (0-8) provides good distribution while keeping management simple.
const DefaultNumShards = 9

// Sharder is the interface for workflow sharding strategies.
// Implementations must be thread-safe.
//
// Example custom implementation:
//
//	type CustomSharder struct {
//		numShards int
//		mu        sync.RWMutex
//	}
//
//	func (s *CustomSharder) GetShard(workflowID uuid.UUID) int {
//		s.mu.RLock()
//		defer s.mu.RUnlock()
//		// Your custom logic here - use first byte of UUID
//		return int(workflowID[0]) % s.numShards
//	}
//
//	func (s *CustomSharder) NumShards() int {
//		s.mu.RLock()
//		defer s.mu.RUnlock()
//		return s.numShards
//	}
//
// Then use it:
//
//	sharder := &CustomSharder{numShards: 12}
//	engine := flows.NewEngine(pool, flows.WithSharder(sharder))
type Sharder interface {
	// GetShard returns the shard number (0-based) for a given workflow ID.
	// Must be deterministic - same workflow ID always returns same shard.
	GetShard(workflowID uuid.UUID) int

	// NumShards returns the total number of shards.
	NumShards() int
}

// ShardConfig holds the sharding configuration for workflow distribution.
// It uses simple modulo-based hashing to distribute workflows across fixed shards.
type ShardConfig struct {
	numShards int
	mu        sync.RWMutex
}

// Ensure ShardConfig implements Sharder interface
var _ Sharder = (*ShardConfig)(nil)

// NewShardConfig creates a new shard configuration with the specified number of shards.
// The number of shards is fixed at creation time and cannot be changed.
func NewShardConfig(numShards int) *ShardConfig {
	if numShards <= 0 {
		numShards = DefaultNumShards
	}
	return &ShardConfig{
		numShards: numShards,
	}
}

// GetShard returns the shard number for a given workflow ID.
// Uses FNV-1a hash with modulo to distribute workflows evenly across shards.
// The shard assignment for a workflow ID is deterministic and stable.
func (s *ShardConfig) GetShard(workflowID uuid.UUID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hasher := fnv.New32a()
	hasher.Write([]byte(workflowID.String()))
	hash := hasher.Sum32()

	return int(hash % uint32(s.numShards))
}

// NumShards returns the number of shards in this configuration.
func (s *ShardConfig) NumShards() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.numShards
}

// NewDefaultShardConfig creates a shard configuration with the default number of shards (9).
func NewDefaultShardConfig() *ShardConfig {
	return NewShardConfig(DefaultNumShards)
}

// Global sharder instance
var (
	globalSharder   Sharder
	globalSharderMu sync.RWMutex
)

// SetSharder sets the global sharder for use with package-level functions.
// This is optional - you can also pass custom sharders to Engine/Worker.
//
// Example with default shard config:
//
//	sharder := flows.NewDefaultShardConfig()
//	flows.SetSharder(sharder)
//	exec, err := flows.Start(ctx, myWorkflow, input)
//
// Example with custom number of shards:
//
//	sharder := flows.NewShardConfig(16) // 16 shards instead of default 9
//	flows.SetSharder(sharder)
//
// Example with custom sharder implementation:
//
//	type MyCustomSharder struct {}
//	func (s *MyCustomSharder) GetShard(workflowID uuid.UUID) int { /* custom logic */ }
//	func (s *MyCustomSharder) NumShards() int { return 10 }
//
//	flows.SetSharder(&MyCustomSharder{})
func SetSharder(sharder Sharder) {
	globalSharderMu.Lock()
	defer globalSharderMu.Unlock()
	globalSharder = sharder
}

// SetShardConfig is a convenience function that sets the global sharder to a ShardConfig.
// For custom sharder implementations, use SetSharder instead.
func SetShardConfig(config *ShardConfig) {
	SetSharder(config)
}

// getGlobalSharder safely retrieves the global sharder.
// If not set, returns a default configuration with 9 shards.
func getGlobalSharder() Sharder {
	globalSharderMu.RLock()
	defer globalSharderMu.RUnlock()

	if globalSharder == nil {
		// Return default shard config if not set
		globalSharderMu.RUnlock()
		globalSharderMu.Lock()
		if globalSharder == nil {
			globalSharder = NewDefaultShardConfig()
		}
		globalSharderMu.Unlock()
		globalSharderMu.RLock()
	}

	return globalSharder
}

// getShardForWorkflow returns the shard number for a given workflow ID.
// If sharder is nil, uses the global sharder.
func getShardForWorkflow(workflowID uuid.UUID, sharder Sharder) int {
	if sharder == nil {
		sharder = getGlobalSharder()
	}
	return sharder.GetShard(workflowID)
}

// getShardedWorkflowName returns the workflow name with shard suffix.
// Format: "{baseName}-shard-{shardNum}"
// Example: "loan-application-workflow" -> "loan-application-workflow-shard-3"
// If sharder is nil, uses the global sharder.
func getShardedWorkflowName(baseName string, workflowID uuid.UUID, sharder Sharder) string {
	shard := getShardForWorkflow(workflowID, sharder)
	return fmt.Sprintf("%s-shard-%d", baseName, shard)
}

// getAllShardedWorkflowNames returns all possible sharded names for a workflow type.
// Workers should poll all shards for their workflow type.
// Example: "loan-application-workflow" -> ["...-shard-0", "...-shard-1", ..., "...-shard-8"]
// If sharder is nil, uses the global sharder.
func getAllShardedWorkflowNames(baseName string, sharder Sharder) []string {
	if sharder == nil {
		sharder = getGlobalSharder()
	}
	numShards := sharder.NumShards()
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
