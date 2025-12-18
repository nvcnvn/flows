package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows"
)

type StressInput struct {
	Seq int64 `json:"seq"`
}

type StressOutput struct {
	Workflow string `json:"workflow"`
	Seq      int64  `json:"seq"`
}

type StressWorkflow struct {
	workflow string
	testID   string
	cpuIters int
	sleep    time.Duration
}

func (w *StressWorkflow) Name() string {
	if w.testID == "" {
		return "stress_" + w.workflow
	}
	return "stress_" + w.workflow + "_" + w.testID
}

func (w *StressWorkflow) Run(ctx context.Context, wf *flows.Context, in *StressInput) (*StressOutput, error) {
	_, err := flows.Execute(ctx, wf, "cpu/v1", func(ctx context.Context, in *StressInput) (*StressInput, error) {
		if w.cpuIters > 0 {
			busyCPU(w.cpuIters)
		}
		return in, nil
	}, in, flows.RetryPolicy{})
	if err != nil {
		return nil, err
	}

	if w.sleep > 0 {
		flows.Sleep(ctx, wf, "sleep/v1", w.sleep)
	}

	return &StressOutput{Workflow: w.workflow, Seq: in.Seq}, nil
}

func busyCPU(iters int) {
	// Simple deterministic CPU loop.
	// Keep it small by default so the bottleneck is coordination/DB.
	v := uint64(1469598103934665603)
	for i := 0; i < iters; i++ {
		v ^= uint64(i) + 0x9e3779b97f4a7c15
		v *= 1099511628211
	}
	_ = v
}

type config struct {
	mode      string
	dbURL     string
	schema    string
	shards    int
	testID    string
	resetDB   bool
	nWorkers  int
	batchSize int

	totalA int
	totalB int
	totalC int
	totalD int

	cpuA int
	cpuB int
	cpuC int
	cpuD int

	sleepA time.Duration
	sleepB time.Duration
	sleepC time.Duration
	sleepD time.Duration

	pollInterval time.Duration
	outDir       string

	dockerStatsInterval time.Duration
	dockerStatsFilter   string
}

func main() {
	cfg := parseFlags()
	ctx := context.Background()

	if cfg.dbURL == "" {
		fatalf("--database-url is required (or set DATABASE_URL)")
	}

	pool, err := pgxpool.New(ctx, cfg.dbURL)
	if err != nil {
		fatalErr(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		fatalErr(err)
	}

	wA := &StressWorkflow{workflow: "A", testID: cfg.testID, cpuIters: cfg.cpuA, sleep: cfg.sleepA}
	wB := &StressWorkflow{workflow: "B", testID: cfg.testID, cpuIters: cfg.cpuB, sleep: cfg.sleepB}
	wC := &StressWorkflow{workflow: "C", testID: cfg.testID, cpuIters: cfg.cpuC, sleep: cfg.sleepC}
	wD := &StressWorkflow{workflow: "D", testID: cfg.testID, cpuIters: cfg.cpuD, sleep: cfg.sleepD}
	workflows := []*StressWorkflow{wA, wB, wC, wD}

	switch cfg.mode {
	case "worker":
		runWorker(ctx, pool, cfg, workflows)
	case "generator":
		runGenerator(ctx, pool, cfg, workflows)
	default:
		fatalf("unknown --mode=%q (want worker|generator)", cfg.mode)
	}
}

func parseFlags() config {
	var cfg config

	flag.StringVar(&cfg.mode, "mode", getenvDefault("FLOWS_STRESS_MODE", "generator"), "generator or worker")
	flag.StringVar(&cfg.dbURL, "database-url", getenvDefault("DATABASE_URL", ""), "Postgres connection string")
	flag.StringVar(&cfg.schema, "schema", getenvDefault("FLOWS_SCHEMA", flows.DefaultSchema), "Postgres schema")
	flag.IntVar(&cfg.shards, "shards", getenvIntDefault("FLOWS_SHARDS", 32), "ShardCount used by flows")
	flag.StringVar(&cfg.testID, "test-id", getenvDefault("FLOWS_TEST_ID", defaultTestID()), "identifier suffix for workflow names")
	flag.BoolVar(&cfg.resetDB, "reset", getenvBoolDefault("FLOWS_RESET", true), "drop + recreate schema before running (generator only)")
	flag.IntVar(&cfg.nWorkers, "workers", getenvIntDefault("FLOWS_WORKERS", 1), "worker concurrency (worker mode only; goroutines in this process)")
	flag.IntVar(&cfg.batchSize, "enqueue-batch", getenvIntDefault("FLOWS_ENQUEUE_BATCH", 50), "runs per transaction while enqueueing")

	flag.IntVar(&cfg.totalA, "total-a", getenvIntDefault("FLOWS_TOTAL_A", 10000), "total runs for workflow A")
	flag.IntVar(&cfg.totalB, "total-b", getenvIntDefault("FLOWS_TOTAL_B", 10000), "total runs for workflow B")
	flag.IntVar(&cfg.totalC, "total-c", getenvIntDefault("FLOWS_TOTAL_C", 10000), "total runs for workflow C")
	flag.IntVar(&cfg.totalD, "total-d", getenvIntDefault("FLOWS_TOTAL_D", 10000), "total runs for workflow D")

	flag.IntVar(&cfg.cpuA, "cpu-a", getenvIntDefault("FLOWS_CPU_A", 0), "CPU iterations per run for workflow A")
	flag.IntVar(&cfg.cpuB, "cpu-b", getenvIntDefault("FLOWS_CPU_B", 0), "CPU iterations per run for workflow B")
	flag.IntVar(&cfg.cpuC, "cpu-c", getenvIntDefault("FLOWS_CPU_C", 0), "CPU iterations per run for workflow C")
	flag.IntVar(&cfg.cpuD, "cpu-d", getenvIntDefault("FLOWS_CPU_D", 0), "CPU iterations per run for workflow D")

	flag.DurationVar(&cfg.sleepA, "sleep-a", getenvDurationDefault("FLOWS_SLEEP_A", 0), "durable sleep per run for workflow A")
	flag.DurationVar(&cfg.sleepB, "sleep-b", getenvDurationDefault("FLOWS_SLEEP_B", 0), "durable sleep per run for workflow B")
	flag.DurationVar(&cfg.sleepC, "sleep-c", getenvDurationDefault("FLOWS_SLEEP_C", 0), "durable sleep per run for workflow C")
	flag.DurationVar(&cfg.sleepD, "sleep-d", getenvDurationDefault("FLOWS_SLEEP_D", 0), "durable sleep per run for workflow D")

	flag.DurationVar(&cfg.pollInterval, "poll", getenvDurationDefault("FLOWS_POLL", 1*time.Second), "poll interval while waiting for completion")
	flag.StringVar(&cfg.outDir, "out", getenvDefault("FLOWS_OUT", "examples/stress/results"), "output directory for results")

	flag.DurationVar(&cfg.dockerStatsInterval, "docker-stats-interval", getenvDurationDefault("FLOWS_DOCKER_STATS_INTERVAL", 0), "if >0, sample `docker stats --no-stream` every interval")
	flag.StringVar(&cfg.dockerStatsFilter, "docker-stats-filter", getenvDefault("FLOWS_DOCKER_STATS_FILTER", ""), "optional substring filter for docker container names (comma-separated)")

	flag.Parse()

	return cfg
}

func runWorker(ctx context.Context, pool *pgxpool.Pool, cfg config, workflows []*StressWorkflow) {
	reg := flows.NewRegistry()
	for _, wf := range workflows {
		// Each workflow gets its own concurrency pool based on nWorkers
		flows.Register(reg, wf, flows.WithConcurrency(cfg.nWorkers))
	}

	clientCfg := flows.DBConfig{Schema: cfg.schema, ShardCount: cfg.shards}

	worker := &flows.Worker{
		Pool:     pool,
		Registry: reg,
		DBConfig: clientCfg,
	}

	fatalErr(worker.Run(ctx))
}

func runGenerator(ctx context.Context, pool *pgxpool.Pool, cfg config, workflows []*StressWorkflow) {
	client := flows.Client{DBConfig: flows.DBConfig{Schema: cfg.schema, ShardCount: cfg.shards}}
	schema := sanitizeSchema(cfg.schema)

	if cfg.resetDB {
		if err := resetSchema(ctx, pool, schema); err != nil {
			fatalErr(err)
		}
		if err := setupCitusSingleNode(ctx, pool); err != nil {
			fatalErr(err)
		}
		if _, err := pool.Exec(ctx, flows.SchemaSQLFor(schema)); err != nil {
			fatalf("create schema: %v", err)
		}
		if _, err := pool.Exec(ctx, flows.CitusSchemaSQLFor(schema)); err != nil {
			fatalf("create citus distributed tables: %v", err)
		}
	}

	_ = os.MkdirAll(cfg.outDir, 0o755)
	resultPath := filepath.Join(cfg.outDir, fmt.Sprintf("%s_results.json", cfg.testID))
	statsPath := filepath.Join(cfg.outDir, fmt.Sprintf("%s_stats.json", cfg.testID))
	dockerStatsPath := filepath.Join(cfg.outDir, fmt.Sprintf("%s_docker_stats.jsonl", cfg.testID))

	var stopDockerStats func()
	if cfg.dockerStatsInterval > 0 {
		stopDockerStats = startDockerStatsSampler(ctx, cfg.dockerStatsInterval, cfg.dockerStatsFilter, dockerStatsPath)
	}
	defer func() {
		if stopDockerStats != nil {
			stopDockerStats()
		}
	}()

	expected := map[string]int{
		workflows[0].Name(): cfg.totalA,
		workflows[1].Name(): cfg.totalB,
		workflows[2].Name(): cfg.totalC,
		workflows[3].Name(): cfg.totalD,
	}

	enqStart := time.Now()
	var enqueued atomic.Int64
	var wg sync.WaitGroup
	wg.Add(4)
	go enqueueLoop(ctx, &wg, pool, client, workflows[0], cfg.totalA, cfg.batchSize, &enqueued)
	go enqueueLoop(ctx, &wg, pool, client, workflows[1], cfg.totalB, cfg.batchSize, &enqueued)
	go enqueueLoop(ctx, &wg, pool, client, workflows[2], cfg.totalC, cfg.batchSize, &enqueued)
	go enqueueLoop(ctx, &wg, pool, client, workflows[3], cfg.totalD, cfg.batchSize, &enqueued)
	wg.Wait()
	enqDur := time.Since(enqStart)

	workflowNames := make([]string, 0, len(expected))
	for name := range expected {
		workflowNames = append(workflowNames, name)
	}

	waitStart := time.Now()
	lastLog := time.Time{}
	var lastCompleted int
	var lastFailed int
	totalExpected := 0
	for _, want := range expected {
		totalExpected += want
	}
	for {
		done, snapshot, err := completionSnapshot(ctx, pool, schema, workflowNames, expected)
		if err != nil {
			fatalErr(err)
		}

		completed := 0
		failed := 0
		for _, r := range snapshot.Rows {
			completed += r.Completed
			failed += r.Failed
		}
		// Emit a small progress line every ~5s so the generator doesn't look hung.
		if lastLog.IsZero() || time.Since(lastLog) >= 5*time.Second {
			noProgress := completed == lastCompleted && failed == lastFailed
			if noProgress {
				fmt.Fprintf(os.Stderr, "waiting... completed=%d/%d failed=%d (no progress)\n", completed, totalExpected, failed)
				fmt.Fprintf(os.Stderr, "hint: check workers are running, and --test-id/--shards match between generator and workers\n")
			} else {
				fmt.Fprintf(os.Stderr, "waiting... completed=%d/%d failed=%d\n", completed, totalExpected, failed)
			}
			lastCompleted = completed
			lastFailed = failed
			lastLog = time.Now()
		}

		b, _ := json.MarshalIndent(snapshot, "", "  ")
		_ = os.WriteFile(resultPath, b, 0o644)

		if done {
			break
		}
		time.Sleep(cfg.pollInterval)
	}
	waitDur := time.Since(waitStart)

	stats, err := workflowStats(ctx, pool, schema, workflowNames)
	if err != nil {
		fatalErr(err)
	}

	out := map[string]any{
		"test_id":        cfg.testID,
		"schema":         schema,
		"shards":         cfg.shards,
		"totals":         expected,
		"enqueued_total": enqueued.Load(),
		"enqueue_time_s": roundFloat(enqDur.Seconds(), 3),
		"wait_time_s":    roundFloat(waitDur.Seconds(), 3),
		"stats":          stats,
		"generated_at":   time.Now().Format(time.RFC3339Nano),
	}

	b, _ := json.MarshalIndent(out, "", "  ")
	_ = os.WriteFile(statsPath, b, 0o644)
	fmt.Println(string(b))
}

func enqueueLoop(ctx context.Context, wg *sync.WaitGroup, pool *pgxpool.Pool, client flows.Client, wf flows.Workflow[StressInput, StressOutput], total int, batchSize int, enqueued *atomic.Int64) {
	defer wg.Done()
	if batchSize <= 0 {
		batchSize = 1
	}

	seq := int64(0)
	remaining := total
	for remaining > 0 {
		batch := batchSize
		if batch > remaining {
			batch = remaining
		}

		err := func() error {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)

			for i := 0; i < batch; i++ {
				seq++
				_, err := flows.BeginTx(ctx, client, tx, wf, &StressInput{Seq: seq})
				if err != nil {
					return err
				}
			}
			return tx.Commit(ctx)
		}()
		if err != nil {
			fatalErr(err)
		}

		remaining -= batch
		enqueued.Add(int64(batch))
	}
}

type completionRow struct {
	WorkflowName string `json:"workflow_name"`
	Total        int    `json:"total"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
}

type completionSnapshotResult struct {
	At      string          `json:"at"`
	Rows    []completionRow `json:"rows"`
	AllDone bool            `json:"all_done"`
}

func completionSnapshot(ctx context.Context, pool *pgxpool.Pool, schema string, workflowNames []string, expected map[string]int) (done bool, snapshot completionSnapshotResult, err error) {
	schema = sanitizeSchema(schema)
	q := fmt.Sprintf(`
SELECT
	workflow_name,
	count(*)::int AS total,
	count(*) FILTER (WHERE status = 'completed')::int AS completed,
	count(*) FILTER (WHERE status = 'failed')::int AS failed
FROM %s.runs
WHERE workflow_name = ANY($1)
GROUP BY workflow_name
`, schema)

	rows, err := pool.Query(ctx, q, workflowNames)
	if err != nil {
		return false, completionSnapshotResult{}, err
	}
	defer rows.Close()

	seen := map[string]completionRow{}
	for rows.Next() {
		var r completionRow
		if err := rows.Scan(&r.WorkflowName, &r.Total, &r.Completed, &r.Failed); err != nil {
			return false, completionSnapshotResult{}, err
		}
		seen[r.WorkflowName] = r
	}
	if err := rows.Err(); err != nil {
		return false, completionSnapshotResult{}, err
	}

	outRows := make([]completionRow, 0, len(workflowNames))
	allDone := true
	for _, name := range workflowNames {
		r := seen[name]
		r.WorkflowName = name
		want := expected[name]
		if r.Total != want {
			allDone = false
		}
		if (r.Completed + r.Failed) != want {
			allDone = false
		}
		outRows = append(outRows, r)
	}

	return allDone, completionSnapshotResult{At: time.Now().Format(time.RFC3339Nano), Rows: outRows, AllDone: allDone}, nil
}

type statsRow struct {
	WorkflowName string  `json:"workflow_name"`
	Completed    int     `json:"completed"`
	Failed       int     `json:"failed"`
	MinMs        float64 `json:"min_ms"`
	P50Ms        float64 `json:"p50_ms"`
	P95Ms        float64 `json:"p95_ms"`
	P99Ms        float64 `json:"p99_ms"`
	AvgMs        float64 `json:"avg_ms"`
	MaxMs        float64 `json:"max_ms"`
}

func workflowStats(ctx context.Context, pool *pgxpool.Pool, schema string, workflowNames []string) ([]statsRow, error) {
	schema = sanitizeSchema(schema)
	q := fmt.Sprintf(`
SELECT
	workflow_name,
	count(*) FILTER (WHERE status = 'completed')::int AS completed,
	count(*) FILTER (WHERE status = 'failed')::int AS failed,
	coalesce(min(extract(epoch from (updated_at - created_at)) * 1000), 0)::float8 AS min_ms,
	coalesce(percentile_cont(0.50) WITHIN GROUP (ORDER BY extract(epoch from (updated_at - created_at)) * 1000), 0)::float8 AS p50_ms,
	coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch from (updated_at - created_at)) * 1000), 0)::float8 AS p95_ms,
	coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY extract(epoch from (updated_at - created_at)) * 1000), 0)::float8 AS p99_ms,
	coalesce(avg(extract(epoch from (updated_at - created_at)) * 1000), 0)::float8 AS avg_ms,
	coalesce(max(extract(epoch from (updated_at - created_at)) * 1000), 0)::float8 AS max_ms
FROM %s.runs
WHERE workflow_name = ANY($1)
	AND status IN ('completed','failed')
GROUP BY workflow_name
ORDER BY workflow_name
`, schema)

	rows, err := pool.Query(ctx, q, workflowNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []statsRow
	for rows.Next() {
		var r statsRow
		if err := rows.Scan(&r.WorkflowName, &r.Completed, &r.Failed, &r.MinMs, &r.P50Ms, &r.P95Ms, &r.P99Ms, &r.AvgMs, &r.MaxMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func resetSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	schema = sanitizeSchema(schema)
	_, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	return err
}

func sanitizeSchema(schema string) string {
	if schema == "" {
		return flows.DefaultSchema
	}
	for i := 0; i < len(schema); i++ {
		b := schema[i]
		isLetter := (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
		isDigit := (b >= '0' && b <= '9')
		if !(isLetter || isDigit || b == '_') {
			return flows.DefaultSchema
		}
		if i == 0 && isDigit {
			return flows.DefaultSchema
		}
	}
	return schema
}

func setupCitusSingleNode(ctx context.Context, pool *pgxpool.Pool) error {
	// Best-effort: these are required for single-node Citus to hold shards.
	_, _ = pool.Exec(ctx, "SELECT citus_set_coordinator_host('localhost', 5432)")
	_, _ = pool.Exec(ctx, "SELECT citus_set_node_property('localhost', 5432, 'shouldhaveshards', true)")
	return nil
}

func startDockerStatsSampler(ctx context.Context, interval time.Duration, filter string, outPath string) func() {
	filters := splitComma(filter)
	stop := make(chan struct{})

	f, err := os.Create(outPath)
	if err != nil {
		fatalf("open docker stats output: %v", err)
	}

	go func() {
		defer f.Close()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				sampleDockerStatsOnce(ctx, f, filters)
			}
		}
	}()

	return func() { close(stop) }
}

func sampleDockerStatsOnce(ctx context.Context, w *os.File, filters []string) {
	cmd := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{json .}}")
	out, err := cmd.Output()
	if err != nil {
		// Non-fatal: docker may not be installed.
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	now := time.Now().Format(time.RFC3339Nano)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if len(filters) > 0 {
			name := extractDockerName(line)
			if !matchesAny(name, filters) {
				continue
			}
		}
		rec := map[string]any{"ts": now}
		var obj any
		if json.Unmarshal([]byte(line), &obj) == nil {
			rec["stats"] = obj
		} else {
			rec["raw"] = line
		}
		b, _ := json.Marshal(rec)
		_, _ = w.Write(append(b, '\n'))
	}
}

func extractDockerName(jsonLine string) string {
	var m map[string]any
	if json.Unmarshal([]byte(jsonLine), &m) != nil {
		return ""
	}
	if v, ok := m["Name"].(string); ok {
		return v
	}
	return ""
}

func matchesAny(s string, subs []string) bool {
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func defaultTestID() string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func getenvBoolDefault(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "1", "t", "true", "y", "yes", "on":
			return true
		case "0", "f", "false", "n", "no", "off":
			return false
		}
	}
	return def
}

func getenvDurationDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return def
}

func roundFloat(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

func fatalErr(err error) {
	fatalf("%v", err)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
