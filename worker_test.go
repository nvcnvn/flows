package flows

import (
	"testing"
	"time"
)

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
