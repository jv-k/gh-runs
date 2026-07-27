package timefmt

import (
	"testing"
	"time"
)

// now0 is the instant every case measures from. It is a literal rather than the wall clock,
// because Age takes now as an argument precisely so that nothing here reads one.
var now0 = time.Date(2026, 7, 15, 16, 39, 0, 0, time.UTC)

// TestAgeBuckets pins the ladder bucket by bucket, on both sides of every boundary it
// changes unit at. It lives here rather than behind a Feed or a table because the ladder is
// pure logic over two instants: asserting it through a rendered row means a bucket change
// reads as a golden diff, and it means one surface's coverage is the other's by accident.
// The Feed keeps one case proving it paints the relative form at all, and the CLI table
// keeps one proving its age column reads this function.
//
// Every bucket truncates rather than rounds, which is why 23h59m is still hours and 29d23h
// is still days.
func TestAgeBuckets(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"a future instant reads as the smallest unit", -time.Hour, "just now"},
		{"a far future instant reads the same", -365 * day, "just now"},
		{"now itself", 0, "just now"},
		{"one second below a minute", time.Minute - time.Second, "just now"},
		{"the minute boundary", time.Minute, "1m"},
		{"one second below an hour", time.Hour - time.Second, "59m"},
		{"the hour boundary", time.Hour, "1h"},
		{"one second below a day", day - time.Second, "23h"},
		{"the day boundary", day, "1d"},
		{"one second below thirty days", 30*day - time.Second, "29d"},
		{"the thirty-day boundary", 30 * day, "1mo"},
		{"one second below a year", 365*day - time.Second, "12mo"},
		{"the year boundary", 365 * day, "1y"},
		{"two years", 730 * day, "2y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Age(now0, now0.Add(-tc.ago)); got != tc.want {
				t.Errorf("Age at %s ago = %q, want %q", tc.ago, got, tc.want)
			}
		})
	}
}

// TestAgeOfAZeroInstantIsEmpty pins the one case that is not a bucket: a Run with no instant
// has no age to state, so the column is blank rather than carrying an age measured from the
// zero time. Without this the answer would be "2026y", which reads as data rather than as
// the absence of it.
func TestAgeOfAZeroInstantIsEmpty(t *testing.T) {
	if got := Age(now0, time.Time{}); got != "" {
		t.Errorf("Age of a zero instant = %q, want the empty string", got)
	}
}

// TestAgeIgnoresTheLocationOfBothInstants pins that the ladder measures a duration and not a
// calendar. A Run's instant arrives from the API in UTC and the injected clock may be in any
// zone, so the same moment expressed two ways has to render one age.
func TestAgeIgnoresTheLocationOfBothInstants(t *testing.T) {
	zone := time.FixedZone("UTC+9", 9*60*60)
	started := now0.Add(-3 * time.Hour)
	if got, want := Age(now0.In(zone), started.In(zone)), Age(now0, started); got != want {
		t.Errorf("the same instants in another zone rendered %q, want %q", got, want)
	}
}
