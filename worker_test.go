package flows

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type noopWorkflow struct {
	name string
}

func (w noopWorkflow) Name() string { return w.name }

func (w noopWorkflow) Run(ctx context.Context, wf *Context, in *struct{}) (*struct{}, error) {
	return &struct{}{}, nil
}

func TestIdleBackoffSequence(t *testing.T) {
	b := newIdleBackoff(250*time.Millisecond, 2*time.Second)

	got := []time.Duration{
		b.next(),
		b.next(),
		b.next(),
		b.next(),
		b.next(),
	}
	want := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		2 * time.Second,
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seq[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	b.reset()
	if got := b.next(); got != 250*time.Millisecond {
		t.Fatalf("after reset next() = %v, want %v", got, 250*time.Millisecond)
	}
}

func TestWorkerMaxPollInterval(t *testing.T) {
	t.Run("default notify-enabled cap", func(t *testing.T) {
		w := Worker{PollInterval: 500 * time.Millisecond}
		if got := w.maxPollInterval(); got != 30*time.Second {
			t.Fatalf("maxPollInterval() = %v, want %v", got, 30*time.Second)
		}
	})

	t.Run("disabled notify stays fixed", func(t *testing.T) {
		w := Worker{PollInterval: 500 * time.Millisecond, DisableNotify: true}
		if got := w.maxPollInterval(); got != 500*time.Millisecond {
			t.Fatalf("maxPollInterval() = %v, want %v", got, 500*time.Millisecond)
		}
	})

	t.Run("configured cap cannot drop below base", func(t *testing.T) {
		w := Worker{PollInterval: 2 * time.Second, MaxPollInterval: 500 * time.Millisecond}
		if got := w.maxPollInterval(); got != 2*time.Second {
			t.Fatalf("maxPollInterval() = %v, want %v", got, 2*time.Second)
		}
	})
}

func measureWorkerClaimAttempts(tb testing.TB, concurrency int, duration time.Duration) int64 {
	tb.Helper()

	registry := NewRegistry()
	Register(registry, noopWorkflow{name: "noop"}, WithConcurrency(concurrency))

	var attempts atomic.Int64
	worker := &Worker{
		Pool:            &pgxpool.Pool{},
		Registry:        registry,
		PollInterval:    5 * time.Millisecond,
		DisableNotify:   true,
		disableCronLoop: true,
		onClaimAttempt: func(workflowName string) {
			attempts.Add(1)
		},
		claimOneForWorkflowFunc: func(ctx context.Context, runner workflowRunner, state *workflowState) (claimedRun, bool, error) {
			return claimedRun{}, false, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Run(ctx)
	}()

	time.Sleep(duration)
	cancel()

	err := <-errCh
	if err != nil && err != context.Canceled {
		tb.Fatalf("worker returned error: %v", err)
	}

	return attempts.Load()
}

func TestWorkerDispatcherClaimVolumeStaysFlatAcrossConcurrency(t *testing.T) {
	one := measureWorkerClaimAttempts(t, 1, 60*time.Millisecond)
	eight := measureWorkerClaimAttempts(t, 8, 60*time.Millisecond)

	if one == 0 || eight == 0 {
		t.Fatalf("expected claim attempts for both runs, got concurrency1=%d concurrency8=%d", one, eight)
	}

	// The dispatcher refactor should keep claim volume roughly flat as executor
	// concurrency increases. Allow some scheduling variance, but not linear growth.
	if eight > one*2 {
		t.Fatalf("expected claim attempts to stay roughly flat across concurrency; concurrency1=%d concurrency8=%d", one, eight)
	}

	t.Logf("claim attempts over 60ms: concurrency1=%d concurrency8=%d", one, eight)
}

func BenchmarkWorkerDispatcherClaimVolume(b *testing.B) {
	const sampleWindow = 60 * time.Millisecond

	for _, concurrency := range []int{1, 8} {
		b.Run(fmt.Sprintf("concurrency_%d", concurrency), func(b *testing.B) {
			b.ReportAllocs()

			var total int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				total += measureWorkerClaimAttempts(b, concurrency, sampleWindow)
			}
			b.StopTimer()

			seconds := float64(sampleWindow) * float64(b.N) / float64(time.Second)
			b.ReportMetric(float64(total)/float64(b.N), "claim_attempts/op")
			b.ReportMetric(float64(total)/seconds, "claim_attempts/s")
		})
	}
}
