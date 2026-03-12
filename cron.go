package flows

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule determines when the next workflow run should be created.
//
// Implementations must be deterministic: the same (after) input must produce the same output.
type Schedule interface {
	// Next returns the earliest time after `after` at which the schedule fires.
	// Returns the zero time if no future fire time exists (shouldn't happen for valid schedules).
	Next(after time.Time) time.Time
}

// Every returns a Schedule that fires at fixed intervals.
//
// Example:
//
//	flows.Every(5 * time.Minute)
func Every(interval time.Duration) Schedule {
	if interval <= 0 {
		panic("flows: Every interval must be positive")
	}
	return everySchedule{interval: interval}
}

type everySchedule struct {
	interval time.Duration
}

func (s everySchedule) Next(after time.Time) time.Time {
	return after.Add(s.interval)
}

// CronSchedule is a parsed 5-field cron expression.
//
// Fields: minute hour day-of-month month day-of-week
//
// Supported syntax per field:
//
//   - any value
//     N        specific value
//     N-M      range (inclusive)
//     N,M,P    list
//     */N      every N
//     N-M/S    range with step
//
// Day-of-week: 0=Sunday, 1=Monday, ..., 6=Saturday (7 is also Sunday).
//
// Special rules (POSIX):
//   - If both day-of-month and day-of-week are restricted (not *),
//     the schedule fires when EITHER matches.
type CronSchedule struct {
	expr    string // original 5-field expression for round-tripping
	minute  cronField
	hour    cronField
	dom     cronField
	month   cronField
	dow     cronField
	domStar bool // true when day-of-month field is unrestricted
	dowStar bool // true when day-of-week field is unrestricted
}

// String returns the original 5-field cron expression.
func (s *CronSchedule) String() string { return s.expr }

// ParseCron parses a standard 5-field cron expression.
//
// Also accepts "@every <duration>" for fixed-interval schedules.
func ParseCron(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)

	// Handle @every shorthand
	if strings.HasPrefix(expr, "@every ") {
		durStr := strings.TrimSpace(expr[7:])
		dur, err := time.ParseDuration(durStr)
		if err != nil {
			return nil, fmt.Errorf("invalid @every duration %q: %w", durStr, err)
		}
		if dur <= 0 {
			return nil, fmt.Errorf("@every duration must be positive, got %v", dur)
		}
		return everySchedule{interval: dur}, nil
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields in cron expression, got %d", len(fields))
	}

	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field %q: %w", fields[0], err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field %q: %w", fields[1], err)
	}
	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field %q: %w", fields[2], err)
	}
	month, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field %q: %w", fields[3], err)
	}
	dow, err := parseField(fields[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field %q: %w", fields[4], err)
	}
	// Normalize: treat 7 (Sunday) the same as 0.
	if dow.has(7) {
		dow.set(0)
	}

	return &CronSchedule{
		expr:    strings.Join(fields, " "),
		minute:  minute,
		hour:    hour,
		dom:     dom,
		month:   month,
		dow:     dow,
		domStar: fields[2] == "*",
		dowStar: fields[4] == "*",
	}, nil
}

// MustParseCron is like ParseCron but panics on error.
func MustParseCron(expr string) Schedule {
	s, err := ParseCron(expr)
	if err != nil {
		panic(fmt.Sprintf("flows: invalid cron expression: %v", err))
	}
	return s
}

// Next returns the earliest time strictly after `after` that matches the schedule.
func (s *CronSchedule) Next(after time.Time) time.Time {
	// Start from the next whole minute.
	t := after.Truncate(time.Minute).Add(time.Minute)
	// Safety: search at most 5 years to avoid infinite loop on impossible expressions.
	limit := t.Add(5 * 366 * 24 * time.Hour)

	for t.Before(limit) {
		// Month
		if !s.month.has(int(t.Month())) {
			// Advance to 1st of next matching month.
			t = s.advanceToNextMonth(t)
			continue
		}

		// Day (with POSIX DOM/DOW interaction)
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			// If we rolled into a new month, re-check month.
			if t.Day() == 1 {
				continue
			}
			continue
		}

		// Hour
		if !s.hour.has(t.Hour()) {
			t = s.advanceToNextHour(t)
			if t.Hour() == 0 {
				// Rolled to next day, re-check day.
				continue
			}
			continue
		}

		// Minute
		if !s.minute.has(t.Minute()) {
			t = s.advanceToNextMinute(t)
			if t.Minute() == 0 {
				// Rolled to next hour, re-check hour.
				continue
			}
			continue
		}

		return t
	}
	return time.Time{}
}

// dayMatches implements POSIX cron day matching:
//   - If both DOM and DOW are *, any day matches.
//   - If only DOM is restricted, only DOM must match.
//   - If only DOW is restricted, only DOW must match.
//   - If both are restricted, EITHER matching is sufficient.
func (s *CronSchedule) dayMatches(t time.Time) bool {
	domMatch := s.dom.has(t.Day())
	dowMatch := s.dow.has(int(t.Weekday()))

	if s.domStar && s.dowStar {
		return true
	}
	if s.domStar {
		return dowMatch
	}
	if s.dowStar {
		return domMatch
	}
	// Both restricted: EITHER.
	return domMatch || dowMatch
}

func (s *CronSchedule) advanceToNextMonth(t time.Time) time.Time {
	// Find next matching month starting from current month+1.
	y, m := t.Year(), t.Month()
	for i := 0; i < 12; i++ {
		m++
		if m > 12 {
			m = 1
			y++
		}
		if s.month.has(int(m)) {
			return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
		}
	}
	// Fallback (shouldn't happen if expression has at least one month).
	return time.Date(y+1, 1, 1, 0, 0, 0, 0, t.Location())
}

func (s *CronSchedule) advanceToNextHour(t time.Time) time.Time {
	for h := t.Hour() + 1; h < 24; h++ {
		if s.hour.has(h) {
			return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
		}
	}
	// No more matching hours today; go to next day at hour 0.
	next := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
	return next
}

func (s *CronSchedule) advanceToNextMinute(t time.Time) time.Time {
	for m := t.Minute() + 1; m < 60; m++ {
		if s.minute.has(m) {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), m, 0, 0, t.Location())
		}
	}
	// No more matching minutes this hour; go to next hour at minute 0.
	next := t.Truncate(time.Hour).Add(time.Hour)
	return next
}

// -- Bit-set field -----------------------------------------------------------

type cronField struct {
	bits uint64 // bit i set ⇒ value i is included
}

func (f cronField) has(val int) bool {
	if val < 0 || val > 63 {
		return false
	}
	return f.bits&(1<<uint(val)) != 0
}

func (f *cronField) set(val int) {
	if val >= 0 && val <= 63 {
		f.bits |= 1 << uint(val)
	}
}

// parseField parses one cron field (e.g., "1,3-5,*/10") into a bit set.
func parseField(field string, min, max int) (cronField, error) {
	var f cronField
	for _, part := range strings.Split(field, ",") {
		if err := parsePart(part, min, max, &f); err != nil {
			return f, err
		}
	}
	if f.bits == 0 {
		return f, fmt.Errorf("field matches no values")
	}
	return f, nil
}

func parsePart(part string, min, max int, f *cronField) error {
	// Split on '/'
	var step int
	stepStr := ""
	if idx := strings.Index(part, "/"); idx >= 0 {
		stepStr = part[idx+1:]
		part = part[:idx]
		s, err := strconv.Atoi(stepStr)
		if err != nil || s <= 0 {
			return fmt.Errorf("invalid step %q", stepStr)
		}
		step = s
	}

	var lo, hi int
	switch {
	case part == "*":
		lo, hi = min, max
	case strings.Contains(part, "-"):
		rng := strings.SplitN(part, "-", 2)
		a, err := strconv.Atoi(rng[0])
		if err != nil {
			return fmt.Errorf("invalid range start %q", rng[0])
		}
		b, err := strconv.Atoi(rng[1])
		if err != nil {
			return fmt.Errorf("invalid range end %q", rng[1])
		}
		if a < min || b > max || a > b {
			return fmt.Errorf("range %d-%d out of bounds [%d, %d]", a, b, min, max)
		}
		lo, hi = a, b
	default:
		v, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("invalid value %q", part)
		}
		if v < min || v > max {
			return fmt.Errorf("value %d out of bounds [%d, %d]", v, min, max)
		}
		if step > 0 {
			lo, hi = v, max
		} else {
			f.set(v)
			return nil
		}
	}

	if step == 0 {
		step = 1
	}
	for v := lo; v <= hi; v += step {
		f.set(v)
	}
	return nil
}
