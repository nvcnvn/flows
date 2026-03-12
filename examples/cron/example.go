package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows"
)

// ---------------------------------------------------------------------------
// Workflow definition (same as any regular workflow)
// ---------------------------------------------------------------------------

type DailyReport struct{}

type DailyReportInput struct {
	ReportType string
}

type DailyReportOutput struct {
	GeneratedAt string
	RowCount    int
}

func (d *DailyReport) Name() string { return "daily_report" }

func (d *DailyReport) Run(ctx context.Context, wf *flows.Context, in *DailyReportInput) (*DailyReportOutput, error) {
	// Step 1: gather data.
	type gatherOut struct{ Rows int }
	out, err := flows.Execute(ctx, wf, "gather_data/v1", func(ctx context.Context, in *DailyReportInput) (*gatherOut, error) {
		fmt.Printf("[%s] gathering data for report type=%s\n", time.Now().Format(time.RFC3339), in.ReportType)
		return &gatherOut{Rows: 42}, nil
	}, in, flows.RetryPolicy{MaxRetries: 2})
	if err != nil {
		return nil, err
	}

	// Step 2: send the report.
	type sendIn struct{ Rows int }
	type sendOut struct{ OK bool }
	_, err = flows.Execute(ctx, wf, "send_report/v1", func(ctx context.Context, in *sendIn) (*sendOut, error) {
		fmt.Printf("[%s] sending report (%d rows)\n", time.Now().Format(time.RFC3339), in.Rows)
		return &sendOut{OK: true}, nil
	}, &sendIn{Rows: out.Rows}, flows.RetryPolicy{MaxRetries: 2})
	if err != nil {
		return nil, err
	}

	return &DailyReportOutput{
		GeneratedAt: time.Now().Format(time.RFC3339),
		RowCount:    out.Rows,
	}, nil
}

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	// -----------------------------------------------------------------------
	// Create schema (in production you'd run migrations separately).
	// -----------------------------------------------------------------------
	if _, err := pool.Exec(ctx, flows.SchemaSQL); err != nil {
		panic(err)
	}

	// -----------------------------------------------------------------------
	// Registry: register the workflow template.
	// -----------------------------------------------------------------------
	reg := flows.NewRegistry()

	report := &DailyReport{}
	flows.Register(reg, report)

	// -----------------------------------------------------------------------
	// Create a cron schedule via the runtime API (ScheduleTx).
	// This can be called at any time — here, at init, or from an HTTP handler.
	// -----------------------------------------------------------------------
	client := flows.Client{}

	tx, err := pool.Begin(ctx)
	if err != nil {
		panic(err)
	}

	// Option A: fixed interval — run every 5 minutes, plus fire once immediately.
	if err := flows.ScheduleTx(ctx, client, tx, report,
		&DailyReportInput{ReportType: "interval"},
		"daily_report_interval",    // unique schedule ID
		flows.Every(5*time.Minute), // fires every 5 min
		flows.WithRunNow(),         // also fire an immediate run
	); err != nil {
		panic(err)
	}

	// Option B: cron expression — run at 02:30 UTC every day.
	// (Uncomment one or both; each needs a unique schedule ID.)
	//
	// if err := flows.ScheduleTx(ctx, client, tx, report,
	// 	&DailyReportInput{ReportType: "nightly"},
	// 	"daily_report_nightly",
	// 	flows.MustParseCron("30 2 * * *"),
	// ); err != nil {
	// 	panic(err)
	// }

	if err := tx.Commit(ctx); err != nil {
		panic(err)
	}

	// -----------------------------------------------------------------------
	// Start the worker. It will:
	//   1. Start a cron polling loop alongside the regular workflow loop.
	//   2. When a schedule is due, create a run and advance next_run_at.
	//   3. The immediate WithRunNow run is also processed.
	// -----------------------------------------------------------------------
	fmt.Println("starting worker — cron schedules will create runs automatically")
	worker := flows.Worker{Pool: pool, Registry: reg}
	if err := worker.Run(ctx); err != nil {
		panic(err)
	}
}
