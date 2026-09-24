package escalation

import (
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/routing"
)

func londonOfficeHours(weekdays ...string) routing.WorkingHoursSet {
	return routing.WorkingHoursSet{
		ID: "office", Name: "Office", Timezone: "Europe/London",
		Intervals: []routing.WeekdayInterval{{Weekdays: weekdays, Start: "09:00", End: "17:00"}},
	}
}

func tokyoOfficeHours() routing.WorkingHoursSet {
	return routing.WorkingHoursSet{
		ID: "tokyo", Name: "Tokyo", Timezone: "Asia/Tokyo",
		Intervals: []routing.WeekdayInterval{{Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "18:00"}},
	}
}

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func TestWindowOpen(t *testing.T) {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	tests := []struct {
		name string
		set  routing.WorkingHoursSet
		now  time.Time
		want bool
	}{
		// 2026-09-14 is a Monday; London is on BST (UTC+1) in September.
		{"inside the window", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 10, 0), true},
		{"at the start minute", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 8, 0), true},
		{"minute before the start", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 7, 59), false},
		{"end minute is closed", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 16, 0), false},
		{"saturday is closed", londonOfficeHours(weekdays...), utc(2026, time.September, 12, 10, 0), false},
		{"saturday interval opens", londonOfficeHours("sat"), utc(2026, time.September, 12, 10, 0), true},
		// Tokyo's day begins while UTC is still on the previous date, so the
		// weekday and clock must both come from the local wall clock.
		{"tokyo tuesday morning before nine", tokyoOfficeHours(), utc(2026, time.September, 14, 23, 30), false},
		{"tokyo tuesday morning after nine", tokyoOfficeHours(), utc(2026, time.September, 15, 0, 30), true},
		{"tokyo saturday while utc is friday", tokyoOfficeHours(), utc(2026, time.September, 18, 22, 0), false},
		{"unknown timezone is closed", routing.WorkingHoursSet{Timezone: "Mars/Olympus", Intervals: []routing.WeekdayInterval{{Weekdays: weekdays, Start: "00:00", End: "23:59"}}}, utc(2026, time.September, 14, 10, 0), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := windowOpen(test.set, test.now); got != test.want {
				t.Fatalf("windowOpen(%s) = %v, want %v", test.now, got, test.want)
			}
		})
	}
}

func TestNextOpen(t *testing.T) {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	everyDay := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	tests := []struct {
		name string
		set  routing.WorkingHoursSet
		now  time.Time
		want time.Time
	}{
		{"already open returns now", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 10, 0), utc(2026, time.September, 14, 10, 0)},
		{"later the same day", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 6, 30), utc(2026, time.September, 14, 8, 0)},
		{"sub-minute now rounds forward to the opening minute", londonOfficeHours(weekdays...), utc(2026, time.September, 14, 7, 59).Add(30 * time.Second), utc(2026, time.September, 14, 8, 0)},
		// Friday 2026-09-11 evening rolls over the weekend to Monday 09:00 BST.
		{"friday evening to monday morning", londonOfficeHours(weekdays...), utc(2026, time.September, 11, 18, 0), utc(2026, time.September, 14, 8, 0)},
		// London springs forward on Sunday 2026-03-29 at 01:00 UTC, so 09:00
		// local that day is 08:00 UTC, not the 09:00 UTC of the day before.
		{"across the spring daylight-saving change", londonOfficeHours(everyDay...), utc(2026, time.March, 28, 20, 0), utc(2026, time.March, 29, 8, 0)},
		{"tokyo monday opens on sunday utc", tokyoOfficeHours(), utc(2026, time.September, 12, 12, 0), utc(2026, time.September, 14, 0, 0)},
		{"unknown timezone falls back to the horizon", routing.WorkingHoursSet{Timezone: "Mars/Olympus"}, utc(2026, time.September, 14, 10, 0), utc(2026, time.September, 22, 10, 0)},
		{"no interval falls back to the horizon", routing.WorkingHoursSet{Timezone: "Europe/London"}, utc(2026, time.September, 14, 10, 0), utc(2026, time.September, 22, 10, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextOpen(test.set, test.now); !got.Equal(test.want) {
				t.Fatalf("nextOpen(%s) = %s, want %s", test.now, got.UTC(), test.want)
			}
		})
	}
}
