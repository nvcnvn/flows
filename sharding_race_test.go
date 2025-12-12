package flows

import (
	"sync"
	"testing"
)

// TestGetGlobalSharderConcurrentInit tests that concurrent calls to getGlobalSharder()
// during initialization don't cause race conditions or panics.
// This test should be run with the race detector: go test -race
func TestGetGlobalSharderConcurrentInit(t *testing.T) {
	t.Parallel()

	// Reset global sharder to nil to test initialization race
	globalSharderMu.Lock()
	originalSharder := globalSharder
	globalSharder = nil
	globalSharderMu.Unlock()

	// Restore after test
	defer func() {
		globalSharderMu.Lock()
		globalSharder = originalSharder
		globalSharderMu.Unlock()
	}()

	var wg sync.WaitGroup
	numGoroutines := 100

	// Start many goroutines that all try to get the global sharder simultaneously
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sharder := getGlobalSharder()
			if sharder == nil {
				t.Error("getGlobalSharder() returned nil")
			}
			// Verify the sharder is functional
			if sharder.NumShards() <= 0 {
				t.Error("NumShards() returned non-positive value")
			}
		}()
	}

	wg.Wait()

	// Verify that the global sharder was initialized
	globalSharderMu.Lock()
	if globalSharder == nil {
		t.Error("globalSharder should have been initialized")
	}
	globalSharderMu.Unlock()
}

// TestGetGlobalSharderConcurrentAccess tests that concurrent read access to
// getGlobalSharder() works correctly after initialization.
func TestGetGlobalSharderConcurrentAccess(t *testing.T) {
	t.Parallel()

	// Ensure global sharder is initialized
	_ = getGlobalSharder()

	var wg sync.WaitGroup
	numGoroutines := 100
	iterations := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				sharder := getGlobalSharder()
				if sharder == nil {
					t.Error("getGlobalSharder() returned nil")
				}
			}
		}()
	}

	wg.Wait()
}
