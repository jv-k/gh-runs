// Package timefmt renders an instant as a compact relative age. It is the one rendering
// two surfaces share: the read-only CLI list's age column (ADR-0023) and the Feed's
// relative timestamp format ([settings] R10). It lives in its own package because both
// need it and neither may import the other (ADR-0011).
//
// It takes now as an argument rather than reading the wall clock. Reading it here would be
// I/O in a formatting package, and it would put a second clock behind surfaces that already
// inject one, so a golden over either would stop being deterministic (ADR-0013).
//
// It is deliberately not gh's timeago wording, which the CLI's -t templates carry
// separately. The two renderings answer the same question at different densities, and
// ADR-0023 keeps them apart on purpose: one clock, and a renderer per density. This is the
// terse one, and it is shared, because a column has a width to fit. The prose one is -t's
// alone, because R7 obliges it to match gh's wording exactly.
package timefmt

import (
	"fmt"
	"time"
)

// Age renders how long ago t was, measured from now, in gh's buckets at the terse width a
// table column can hold: minutes below an hour, hours below a day, days below thirty days,
// months below a year, and years above it. Each bucket truncates rather than rounds.
//
// A zero t renders empty, because a Run with no instant has no age to state. A future t
// renders as the smallest unit rather than a negative age: clock skew between this machine
// and GitHub's is real, and "in -3 minutes" reads as a bug where "just now" reads as new.
func Age(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
	}
}
