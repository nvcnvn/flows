package flows

import (
	"testing"
	"time"
)

func TestParseCron_Every(t *testing.T) {
	s, err := ParseCron("@every 5m")
	if err != nil {
		t.Fatalf("ParseCron(@every 5m): %v", err)
	}
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	next := s.Next(now)
	want := now.Add(5 * time.Minute)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestParseCron_EveryInvalid(t *testing.T) {
	for _, expr := range []string{"@every -1s", "@every 0s", "@every abc"} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("expected error for %q", expr)
		}
	}
}

func TestParseCron_InvalidFieldCount(t *testing.T) {
	if _, err := ParseCron("* * *"); err == nil {
		t.Error("expected error for 3-field expression")
	}
}

func TestCronSchedule_EveryMinute(t *testing.T) {
	s := MustParseCron("* * * * *")
	now := time.Date(2026, 3, 12, 10, 30, 15, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 3, 12, 10, 31, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_SpecificMinuteHour(t *testing.T) {
	// At 9:30 every day
	s := MustParseCron("30 9 * * *")

	// Before the time today
	now := time.Date(2026, 3, 12, 8, 0, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 3, 12, 9, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("before: Next = %v, want %v", next, want)
	}

	// After the time today → should go to tomorrow
	now = time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	next = s.Next(now)
	want = time.Date(2026, 3, 13, 9, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("after: Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_EveryFiveMinutes(t *testing.T) {
	// */5 * * * * → minutes 0,5,10,...,55
	s := MustParseCron("*/5 * * * *")

	now := time.Date(2026, 3, 12, 10, 12, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 3, 12, 10, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_DayOfWeek(t *testing.T) {
	// Every Monday at 9:00 → 0 9 * * 1
	s := MustParseCron("0 9 * * 1")

	// Thursday 2026-03-12
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	next := s.Next(now)
	// Next Monday is 2026-03-16
	want := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_DayOfMonth(t *testing.T) {
	// 1st of every month at midnight → 0 0 1 * *
	s := MustParseCron("0 0 1 * *")

	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_SpecificMonth(t *testing.T) {
	// January 1st at midnight → 0 0 1 1 *
	s := MustParseCron("0 0 1 1 *")

	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_Range(t *testing.T) {
	// Every minute from hour 9-17 on weekdays → * 9-17 * * 1-5
	s := MustParseCron("0 9-17 * * 1-5")

	// Saturday 8:00
	now := time.Date(2026, 3, 14, 8, 0, 0, 0, time.UTC)
	next := s.Next(now)
	// Monday 2026-03-16
	want := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_List(t *testing.T) {
	// Minutes 0,15,30,45 every hour → 0,15,30,45 * * * *
	s := MustParseCron("0,15,30,45 * * * *")

	now := time.Date(2026, 3, 12, 10, 16, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 3, 12, 10, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_Sunday7(t *testing.T) {
	// Sunday can be 0 or 7 → 0 9 * * 7 should fire on Sunday
	s := MustParseCron("0 9 * * 7")

	// Thursday 2026-03-12
	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	next := s.Next(now)
	// Sunday is 2026-03-15
	want := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v (weekday=%d)", next, want, next.Weekday())
	}
}

func TestCronSchedule_BothDOMAndDOW(t *testing.T) {
	// POSIX rule: when both DOM and DOW are restricted, EITHER match fires.
	// "0 0 15 * 1" → midnight on the 15th OR any Monday, whichever is sooner.
	s := MustParseCron("0 0 15 * 1")

	// Thursday 2026-03-12
	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	next := s.Next(now)
	// 15th is Sunday, Monday is 16th → 15th fires first (DOM match)
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_StepWithRange(t *testing.T) {
	// 1-30/10 → 1,11,21
	s := MustParseCron("1-30/10 * * * *")

	now := time.Date(2026, 3, 12, 10, 5, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 3, 12, 10, 11, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_Deterministic(t *testing.T) {
	s := MustParseCron("*/10 * * * *")
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)

	next1 := s.Next(now)
	next2 := s.Next(now)
	if !next1.Equal(next2) {
		t.Errorf("Non-deterministic: %v != %v", next1, next2)
	}
}

func TestCronSchedule_Sequence(t *testing.T) {
	// Every 15 minutes
	s := MustParseCron("*/15 * * * *")

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	expected := []time.Time{
		time.Date(2026, 3, 12, 10, 15, 0, 0, time.UTC),
		time.Date(2026, 3, 12, 10, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 12, 10, 45, 0, 0, time.UTC),
		time.Date(2026, 3, 12, 11, 0, 0, 0, time.UTC),
	}

	for i, want := range expected {
		got := s.Next(now)
		if !got.Equal(want) {
			t.Errorf("seq[%d]: Next(%v) = %v, want %v", i, now, got, want)
		}
		now = got
	}
}

func TestEverySchedule(t *testing.T) {
	s := Every(30 * time.Second)
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	next := s.Next(now)
	want := now.Add(30 * time.Second)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestParseCron_InvalidExpressions(t *testing.T) {
	invalids := []string{
		"60 * * * *",  // minute out of range
		"* 25 * * *",  // hour out of range
		"* * 0 * *",   // day-of-month out of range (min is 1)
		"* * * 13 *",  // month out of range
		"* * * * 8",   // day-of-week out of range
		"*/0 * * * *", // step 0 invalid
		"abc * * * *", // non-numeric
		"5-2 * * * *", // reversed range
		"* * * *",     // too few fields
		"* * * * * *", // too many fields
	}
	for _, expr := range invalids {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("expected error for %q", expr)
		}
	}
}

func TestMustParseCron_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid expression")
		}
	}()
	MustParseCron("invalid")
}

func TestEvery_PanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for zero interval")
		}
	}()
	Every(0)
}

func TestCronSchedule_MonthRollover(t *testing.T) {
	// At 23:59 on Dec 31st → next fire is Jan
	s := MustParseCron("0 0 * * *")

	now := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_HourRolloverToNextDay(t *testing.T) {
	// At hour 9 → if it's already 10, next is tomorrow 9:00
	s := MustParseCron("0 9 * * *")

	now := time.Date(2026, 3, 12, 9, 30, 0, 0, time.UTC)
	next := s.Next(now)
	want := time.Date(2026, 3, 13, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}
