package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// SetupTestDB creates a test database connection and applies migrations
func SetupTestDB(t *testing.T) *pgxpool.Pool {
	ctx := context.Background()

	// Get connection string from environment
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://postgres:postgres@localhost:5432/flows_test?sslmode=disable"
	}

	// Connect to database
	pool, err := pgxpool.New(ctx, connString)
	require.NoError(t, err, "Failed to connect to database")

	// Verify connection with retries (for CI environments)
	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		err = pool.Ping(ctx)
		if err == nil {
			break
		}
		if i == maxRetries-1 {
			require.NoError(t, err, "Failed to ping database after retries")
		}
		t.Logf("Waiting for database... (attempt %d/%d)", i+1, maxRetries)
		time.Sleep(2 * time.Second)
	}

	// Cleanup function
	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

// EnsureDocker checks if Docker is available
func EnsureDocker(t *testing.T) {
	cmd := exec.Command("docker", "--version")
	err := cmd.Run()
	if err != nil {
		t.Skip("Docker is not available, skipping test")
	}
}

// StartDockerCompose starts docker-compose services
func StartDockerCompose(t *testing.T) {
	EnsureDocker(t)

	// Find docker-compose.yml
	composeFile := findDockerComposeFile(t)

	t.Logf("Starting docker-compose from %s", composeFile)

	cmd := exec.Command("docker-compose", "-f", composeFile, "up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	require.NoError(t, err, "Failed to start docker-compose")

	// Wait for postgres to be ready
	time.Sleep(5 * time.Second)

	t.Cleanup(func() {
		StopDockerCompose(t, composeFile)
	})
}

// StopDockerCompose stops docker-compose services
func StopDockerCompose(t *testing.T, composeFile string) {
	cmd := exec.Command("docker-compose", "-f", composeFile, "down")
	err := cmd.Run()
	if err != nil {
		t.Logf("Warning: Failed to stop docker-compose: %v", err)
	}
}

// findDockerComposeFile finds the docker-compose.yml file
func findDockerComposeFile(t *testing.T) string {
	currentDir, err := os.Getwd()
	require.NoError(t, err, "Failed to get current directory")

	dir := currentDir
	for {
		composePath := filepath.Join(dir, "docker-compose.yml")
		if _, err := os.Stat(composePath); err == nil {
			return composePath
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("Could not find docker-compose.yml from %s", currentDir)
	return ""
}
