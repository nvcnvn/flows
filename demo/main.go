package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows"
)

// Server holds shared resources for the application
// This demonstrates the instance-based pattern where a single pool, engine,
// and shard config are created once and reused throughout the application.
type Server struct {
	pool        *pgxpool.Pool
	engine      *flows.Engine
	shardConfig *flows.ShardConfig
	worker      *flows.Worker
}

func main() {
	ctx := context.Background()

	// Connect to PostgreSQL
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("✅ Connected to database")

	// Create sharder (9 shards by default, or customize)
	// In production, use the same sharder across all instances
	sharder := flows.NewShardConfig(16) // Use 16 shards for better scaling
	log.Printf("✅ Created sharder with %d shards", sharder.NumShards())

	// Create engine with custom sharder
	// This engine instance will be reused for all workflow operations
	engine := flows.NewEngine(pool, flows.WithSharder(sharder))
	log.Println("✅ Created workflow engine")

	// Create server with shared resources
	server := &Server{
		pool:        pool,
		engine:      engine,
		shardConfig: sharder,
	}

	// Note: Workflows and activities are registered in workflow.go and activities.go

	// Start worker with the same sharder
	// This ensures worker polls the correct sharded workflow queues
	server.worker = flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:       5,
		WorkflowNames:     []string{"loan-application"},
		PollInterval:      1 * time.Second,
		VisibilityTimeout: 5 * time.Minute,
		Sharder:           sharder, // Use same sharder as engine
	})

	go func() {
		log.Println("🚀 Starting workflow worker...")
		if err := server.worker.Run(ctx); err != nil {
			log.Printf("Worker error: %v", err)
		}
	}()

	// Setup HTTP server
	mux := http.NewServeMux()

	// API routes - workflow name is now in path for clarity
	// Handler methods have access to server.engine for explicit API calls
	mux.HandleFunc("POST /api/loans", server.createLoanHandler)
	mux.HandleFunc("GET /api/loans/{workflowName}/{id}", server.getLoanStatusHandler)
	mux.HandleFunc("POST /api/loans/{workflowName}/{id}/documents", server.submitDocumentHandler)
	mux.HandleFunc("POST /api/loans/{workflowName}/{id}/approve", server.submitApprovalHandler)
	mux.HandleFunc("GET /api/loans/{workflowName}/{id}/result", server.getLoanResultHandler)

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Start HTTP server
	httpServer := &http.Server{
		Addr:    ":8081",
		Handler: loggingMiddleware(mux),
	}

	// Graceful shutdown
	go func() {
		log.Println("🌐 Server starting on http://localhost:8081")
		log.Printf("📊 Using %d shards for workflow distribution", sharder.NumShards())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("🛑 Shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server.worker.Stop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("✅ Shutdown complete")
}

// loggingMiddleware logs all HTTP requests
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("→ %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
		log.Printf("← %s %s (took %v)", r.Method, r.URL.Path, time.Since(start))
	})
}

// writeJSON writes JSON response
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errcheck
	json.NewEncoder(w).Encode(data)
}

// writeError writes error response
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
