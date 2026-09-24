package escalation

import (
	"time"

	// The embedded zone database makes window arithmetic identical on every
	// host, including minimal containers that ship no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/withHumanAI/withHuman/routing"
)

// nextOpenHorizon bounds the forward scan for the next open instant. A
// validated set always has at least one interval on some weekday, so a
// window opens within seven days; the eighth day is slack for zone
// transitions on the boundary.
const nextOpenHorizon = 8 * 24 * time.Hour

var weekdayNames = map[time.Weekday]string{
	time.Monday:    "mon",
	time.Tuesday:   "tue",
	time.Wednesday: "wed",
	time.Thursday:  "thu",
	time.Friday:    "fri",
	time.Saturday:  "sat",
	time.Sunday:    "sun",
}

// windowOpen reports whether now falls inside one of the set's intervals,
// judged on the wall clock of the set's own timezone. An unknown timezone
// never validates, so it is treated as permanently closed rather than
// guessed.
func windowOpen(set routing.WorkingHoursSet, now time.Time) bool {
	location, err := time.LoadLocation(set.Timezone)
	if err != nil {
		return false
	}
	return openAt(set, now.In(location))
}

// openAt is windowOpen for an instant already expressed in the set's
// timezone, so a scan can load the location once.
func openAt(set routing.WorkingHoursSet, local time.Time) bool {
	day := weekdayNames[local.Weekday()]
	clock := local.Format("15:04")
	for _, interval := range set.Intervals {
		if !containsWeekday(interval.Weekdays, day) {
			continue
		}
		// HH:MM strings compare lexically in clock order, which is why the
		// document validates their shape so strictly.
		if interval.Start <= clock && clock < interval.End {
			return true
		}
	}
	return false
}

func containsWeekday(weekdays []string, day string) bool {
	for _, candidate := range weekdays {
		if candidate == day {
			return true
		}
	}
	return false
}

// nextOpen returns the first instant at or after now when the window is
// open. It walks forward one minute at a time on the absolute clock and
// judges each minute on the local wall clock, so daylight-saving gaps and
// overlaps fall out naturally: a wall-clock hour that does not exist is
// simply never observed. The result keeps now's location.
func nextOpen(set routing.WorkingHoursSet, now time.Time) time.Time {
	location, err := time.LoadLocation(set.Timezone)
	if err != nil {
		return now.Add(nextOpenHorizon)
	}
	if openAt(set, now.In(location)) {
		return now
	}
	limit := now.Add(nextOpenHorizon)
	for candidate := now.Truncate(time.Minute).Add(time.Minute); !candidate.After(limit); candidate = candidate.Add(time.Minute) {
		if openAt(set, candidate.In(location)) {
			return candidate
		}
	}
	return now.Add(nextOpenHorizon)
}
