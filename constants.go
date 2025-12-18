package flows

import "unicode"

const (
	runStatusQueued       = "queued"
	runStatusRunning      = "running"
	runStatusSleeping     = "sleeping"
	runStatusWaitingEvent = "waiting_event"
	runStatusCompleted    = "completed"
	runStatusFailed       = "failed"
	runStatusCancelled    = "cancelled"
)

const (
	// notifyChannelRunWakeup is used with LISTEN/NOTIFY to hint workers to re-scan for runnable runs.
	// Notifications are best-effort; workers must still poll as a fallback.
	notifyChannelRunWakeup = "flows_run_wakeup"
)

func normalizeNotifyChannel(ch string) string {
	if ch == "" {
		return notifyChannelRunWakeup
	}
	// LISTEN channel is an identifier; keep this conservative to avoid injection.
	for _, r := range ch {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return notifyChannelRunWakeup
		}
	}
	return ch
}

const (
	stepStatusCompleted = "completed"
	stepStatusFailed    = "failed"
)

const (
	waitTypeSleep = "sleep"
	waitTypeEvent = "event"
)
