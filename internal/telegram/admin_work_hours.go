package telegram

import (
	"strings"
	"time"
)

const adminWorkStartHour = 9
const adminWorkEndHour = 19

// The agreed calendar is daily, 09:00 inclusive to 19:00 exclusive, Moscow.
// A fixed Moscow zone makes it independent of the server's local timezone.
func adminWorkTime(at time.Time) bool {
	hour := at.In(visitTZ).Hour()
	return hour >= adminWorkStartHour && hour < adminWorkEndHour
}

// Count only the intersection with each day's work window. Since this daily
// Moscow calendar has no DST, full days can be counted in constant time.
func adminWorkingDuration(start, end time.Time) time.Duration {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return 0
	}
	start = start.In(visitTZ)
	end = end.In(visitTZ)
	midnight := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, visitTZ) }
	workDay := time.Duration(adminWorkEndHour-adminWorkStartHour) * time.Hour
	withinDay := func(t time.Time) time.Duration {
		elapsed := t.Sub(midnight(t).Add(adminWorkStartHour * time.Hour))
		if elapsed < 0 {
			return 0
		}
		if elapsed > workDay {
			return workDay
		}
		return elapsed
	}
	days := midnight(end).Sub(midnight(start)) / (24 * time.Hour)
	return time.Duration(days)*workDay + withinDay(end) - withinDay(start)
}

func adminReminderStage(created, now time.Time) string {
	if !adminWorkTime(now) {
		return ""
	}
	elapsed := adminWorkingDuration(created, now)
	if elapsed >= supplementEscalationDelay {
		return "escalation"
	}
	if elapsed >= supplementReminderDelay {
		return "reminder"
	}
	return ""
}

func isAdminReminderKey(key string) bool {
	// Preserve the per-event delivery keys already stored in runtime.
	return strings.HasPrefix(key, "case:") || strings.Contains(key, ":reminder:") || strings.Contains(key, ":escalation:")
}
