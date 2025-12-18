package main

import (
	"context"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows"
)

type DemoProcess struct {
	// Put shared resources here (API clients, config, keys, etc.).
	// Keep it concurrency-safe: the same DemoProcess value can be used by multiple workers.
	apiKey string
}

type DemoProcessInput struct {
	InitialData string
}

type DemoProcessOutput struct {
	Result string
}

type firstStepInput struct {
	InitialData string
}

type firstStepOutput struct {
	Number int
}

func NewDemoProcess() *DemoProcess {
	return &DemoProcess{apiKey: os.Getenv("DEMO_API_KEY")}
}

func (dp *DemoProcess) Name() string { return "demo_process" }

// Steps as methods is usually the nicest DX in Go:
// - easy access to shared resources via `dp.*`
// - still type-safe (no casting)
// - works with `flows.Execute` because method values match `flows.Step[I,O]`
func (dp *DemoProcess) firstStep(ctx context.Context, in *firstStepInput) (*firstStepOutput, error) {
	_ = dp.apiKey
	// You can also use the workflow transaction when needed via wf.Tx().
	// (This example doesn't write to DB in steps.)
	return &firstStepOutput{Number: 42}, nil
}

func (dp *DemoProcess) secondStep(ctx context.Context, in *int) (*bool, error) {
	_ = dp.apiKey
	return ptr(true), nil
}

func (dp *DemoProcess) thirdStep(ctx context.Context, in *string) (*string, error) {
	_ = dp.apiKey
	return ptr("Process Completed"), nil
}

func (dp *DemoProcess) Run(ctx context.Context, wf *flows.Context, in *DemoProcessInput) (*DemoProcessOutput, error) {
	// Durable step execution.
	firstOut, err := flows.Execute(ctx, wf,
		"first_step/v1",
		dp.firstStep,
		&firstStepInput{InitialData: in.InitialData},
		flows.RetryPolicy{MaxRetries: 3},
	)
	if err != nil {
		return nil, err
	}

	// Durably yield until wake.
	flows.Sleep(ctx, wf, "sleep_before_customer/v1", 5*time.Second)

	// Wait for external event, then run next step.
	n := flows.WaitForEvent[int](ctx, wf, "customer_number/v1", "CustomerNumberEvent")
	_, err = flows.Execute(ctx, wf,
		"second_step/v1",
		dp.secondStep,
		n,
		flows.RetryPolicy{MaxRetries: 3},
	)
	if err != nil {
		return nil, err
	}

	// Deterministic randomness (same on replay).
	uuid := flows.RandomUUIDv7(ctx, wf, "uuid_for_third_step/v1")
	_, err = flows.Execute(ctx, wf,
		"third_step/v1",
		dp.thirdStep,
		&uuid,
		flows.RetryPolicy{MaxRetries: 3},
	)
	if err != nil {
		return nil, err
	}

	return &DemoProcessOutput{Result: "first=" + itoa(firstOut.Number) + ", uuid=" + uuid}, nil
}

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	reg := flows.NewRegistry()
	flows.Register(reg, NewDemoProcess())

	client := flows.Client{}

	// Enqueue a run inside a transaction (ACID-friendly).
	var runKey flows.RunKey
	err = func() error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		runKey, err = flows.BeginTx(ctx, client, tx, NewDemoProcess(), &DemoProcessInput{InitialData: "hello"})
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		panic(err)
	}

	// Start a worker (in real apps, this runs in a separate process).
	worker := flows.Worker{Pool: pool, Registry: reg}
	go func() {
		_ = worker.Run(ctx)
	}()

	// Simulate publishing an event (in a real app this happens via API).
	_ = func() error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		v := 3
		if err := flows.PublishEventTx(ctx, client, tx, runKey, "CustomerNumberEvent", &v); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()

	// Block a bit so the example doesn't exit immediately.
	<-time.After(2 * time.Second)
}

func ptr[T any](v T) *T {
	return &v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	v := n
	if v < 0 {
		v = -v
	}
	for v > 0 {
		d := v % 10
		s = string(rune('0'+d)) + s
		v /= 10
	}
	if n < 0 {
		s = "-" + s
	}
	return s
}
