package examples_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	sharedPool     *pgxpool.Pool
	sharedPoolOnce sync.Once
	sharedPoolMu   sync.Mutex
)

// SetupTestDB returns a shared database connection pool for all tests
// This simulates production where multiple processes share the same database
func SetupTestDB(t *testing.T) *pgxpool.Pool {
	sharedPoolMu.Lock()
	defer sharedPoolMu.Unlock()

	sharedPoolOnce.Do(func() {
		ctx := context.Background()

		// Get connection string from environment
		connString := os.Getenv("DATABASE_URL")
		if connString == "" {
			connString = "postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable"
		}

		// Configure pool for heavy parallel test workloads
		cfg, err := pgxpool.ParseConfig(connString)
		if err != nil {
			panic("Failed to parse DATABASE_URL: " + err.Error())
		}
		cfg.MaxConns = 80
		cfg.MinConns = 10
		cfg.HealthCheckPeriod = 15 * time.Second
		cfg.MaxConnIdleTime = 30 * time.Second
		cfg.MaxConnLifetime = time.Hour

		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			panic("Failed to connect to database: " + err.Error())
		}

		// Verify connection with retries (for CI environments)
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			err = pool.Ping(ctx)
			if err == nil {
				break
			}
			if i == maxRetries-1 {
				panic("Failed to ping database after retries: " + err.Error())
			}
			time.Sleep(2 * time.Second)
		}

		sharedPool = pool
	})

	return sharedPool
}
