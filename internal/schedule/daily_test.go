package schedule

import (
	"testing"
	"time"
)

func TestDailyDST(t *testing.T) {
	for _, tc := range []struct{ name, clock, now, previous, next string }{
		{"spring evening", "23:00", "2026-03-08T04:00:00Z", "2026-03-08T04:00:00Z", "2026-03-09T03:00:00Z"},
		{"fall evening", "23:00", "2026-11-01T03:00:00Z", "2026-11-01T03:00:00Z", "2026-11-02T04:00:00Z"},
		{"gap", "02:30", "2026-03-08T06:59:00Z", "2026-03-07T07:30:00Z", "2026-03-08T07:00:00Z"},
		{"fold first", "01:30", "2026-11-01T05:29:00Z", "2026-10-31T05:30:00Z", "2026-11-01T05:30:00Z"},
		{"fold second must not repeat", "01:30", "2026-11-01T06:15:00Z", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Parse(tc.clock, "America/New_York")
			if err != nil {
				t.Fatal(err)
			}
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			prev, next := s.Around(now)
			if prev.UTC().Format(time.RFC3339) != tc.previous || next.UTC().Format(time.RFC3339) != tc.next {
				t.Fatalf("previous=%s next=%s", prev, next)
			}
		})
	}
}

func TestInvalidSchedule(t *testing.T) {
	for _, value := range []string{"", "7:00", "24:00", "23:60", "11pm", "23:00:00"} {
		if _, err := Parse(value, "America/New_York"); err == nil {
			t.Errorf("accepted clock %q", value)
		}
	}
	for _, zone := range []string{"", "Local", "not/a-zone", "../UTC"} {
		if _, err := Parse("23:00", zone); err == nil {
			t.Errorf("accepted timezone %q", zone)
		}
	}
}
