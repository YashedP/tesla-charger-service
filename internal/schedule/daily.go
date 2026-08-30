// Package schedule resolves daily wall-clock times, including DST transitions.
package schedule

import (
	"fmt"
	"time"
	_ "time/tzdata" // The runtime image does not need an OS timezone database.
)

type Daily struct {
	Clock    string
	Location *time.Location
	minute   int
}

func Parse(clock, timezone string) (Daily, error) {
	t, err := time.Parse("15:04", clock)
	if err != nil || t.Format("15:04") != clock {
		return Daily{}, fmt.Errorf("CHARGING_CHECK_TIME must use HH:MM (00:00 through 23:59)")
	}
	if timezone == "" || timezone == "Local" {
		return Daily{}, fmt.Errorf("CHARGING_CHECK_TIMEZONE must name an IANA timezone")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return Daily{}, fmt.Errorf("invalid CHARGING_CHECK_TIMEZONE")
	}
	return Daily{Clock: clock, Location: loc, minute: t.Hour()*60 + t.Minute()}, nil
}

// OnDate searches real instants in order, rather than relying on time.Date's
// unspecified choice during DST gaps and folds. A gap uses the first valid
// minute after the requested time; a fold uses its first occurrence.
func (s Daily) OnDate(date time.Time) time.Time {
	y, m, d := date.Date()
	anchor := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for instant := anchor.Add(-24 * time.Hour); instant.Before(anchor.Add(48 * time.Hour)); instant = instant.Add(time.Minute) {
		local := instant.In(s.Location)
		ly, lm, ld := local.Date()
		if ly == y && lm == m && ld == d && local.Hour()*60+local.Minute() >= s.minute {
			return local
		}
	}
	// Entire skipped civil dates (for example Pacific/Apia in 2011) have no run.
	return time.Time{}
}

// Around returns the most recent scheduled occurrence and the next one.
func (s Daily) Around(now time.Time) (time.Time, time.Time) {
	local := now.In(s.Location)
	y, m, d := local.Date()
	date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	var previous, next time.Time
	for offset := -2; offset <= 2; offset++ {
		at := s.OnDate(date.AddDate(0, 0, offset))
		if at.IsZero() {
			continue
		}
		if !at.After(now) {
			previous = at
		} else if next.IsZero() {
			next = at
		}
	}
	return previous, next
}
