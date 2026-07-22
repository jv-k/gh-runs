package filter

import (
	"fmt"
	"strings"
	"time"
)

// DateRange is a parse-validated Created clause (ADR-0016). It holds the
// verbatim string for the wire and the typed bounds for Match, both produced by
// the one parse, so the pair cannot drift and no re-serialisation can shift a
// boundary. Its fields are unexported, and the zero value is no clause.
//
// The interval is half-open, [lo, hi): lo is inclusive, hi is exclusive, and a
// zero bound is unbounded on that side. That single shape expresses every gh
// form, because "after the whole day" and "on or before the whole day" are just
// which day-edge each operator picks.
//
// The boundary atom is provisional and unmeasured. The canon pins the field
// (created_at), the zone (UTC), and gh's syntax, not the atom itself: the
// half-open shape, a bare date spanning the whole UTC day, and a datetime
// spanning one second are our reading of gh, not a measurement of it. A
// flag-then-verify against gh settles it when --created becomes a flag at stage
// 6 (cli-list), and the gap is recorded here so it is a known unknown rather
// than a surprise.
type DateRange struct {
	raw string    // verbatim, exactly as accepted, for the created query parameter
	lo  time.Time // inclusive lower bound; the zero time is unbounded below
	hi  time.Time // exclusive upper bound; the zero time is unbounded above
}

// contains reports whether an instant falls in the range. It compares in UTC
// against created_at (ADR-0016): the comparison is instant-based, so a Run's
// stored zone does not move the boundary.
func (d DateRange) contains(t time.Time) bool {
	u := t.UTC()
	if !d.lo.IsZero() && u.Before(d.lo) {
		return false
	}
	if !d.hi.IsZero() && !u.Before(d.hi) {
		return false
	}
	return true
}

// empty reports the zero value, which is no clause. Only the zero DateRange and
// no parsed clause has an unset raw string, so raw is the honest sentinel.
func (d DateRange) empty() bool { return d.raw == "" }

// ParseCreated validates gh's date syntax and returns the range (ADR-0016). It
// accepts a bare date, the comparison operators >=, <=, > and < on a date, the
// A..B range with * for an open side, and an optional RFC3339 time component. It
// rejects anything else by name, at construction, before any request (cli-surface
// R6). gh's --created is absolute-only, so no clock is consulted.
func ParseCreated(s string) (DateRange, error) {
	if before, after, ok := strings.Cut(s, ".."); ok {
		return parseRange(s, before, after)
	}
	switch {
	case strings.HasPrefix(s, ">="):
		start, _, err := tokenEdges(s[2:])
		if err != nil {
			return DateRange{}, invalidCreated(s)
		}
		return DateRange{raw: s, lo: start}, nil
	case strings.HasPrefix(s, "<="):
		_, end, err := tokenEdges(s[2:])
		if err != nil {
			return DateRange{}, invalidCreated(s)
		}
		return DateRange{raw: s, hi: end}, nil
	case strings.HasPrefix(s, ">"):
		_, end, err := tokenEdges(s[1:])
		if err != nil {
			return DateRange{}, invalidCreated(s)
		}
		return DateRange{raw: s, lo: end}, nil
	case strings.HasPrefix(s, "<"):
		start, _, err := tokenEdges(s[1:])
		if err != nil {
			return DateRange{}, invalidCreated(s)
		}
		return DateRange{raw: s, hi: start}, nil
	default:
		start, end, err := tokenEdges(s)
		if err != nil {
			return DateRange{}, invalidCreated(s)
		}
		return DateRange{raw: s, lo: start, hi: end}, nil
	}
}

// parseRange handles the A..B form. Either side may be * for an open bound, but
// not both, and an empty side is rejected: gh spells an open range with *.
func parseRange(raw, before, after string) (DateRange, error) {
	d := DateRange{raw: raw}
	if before != "*" {
		start, _, err := tokenEdges(before)
		if err != nil {
			return DateRange{}, invalidCreated(raw)
		}
		d.lo = start
	}
	if after != "*" {
		_, end, err := tokenEdges(after)
		if err != nil {
			return DateRange{}, invalidCreated(raw)
		}
		d.hi = end
	}
	if d.lo.IsZero() && d.hi.IsZero() {
		return DateRange{}, invalidCreated(raw)
	}
	return d, nil
}

// tokenEdges parses one date or datetime token into its half-open atom
// [start, end), in UTC. A bare date is the whole UTC day, so its atom is a day
// wide; a datetime is the one second at its stated precision, matching the API's
// second-granularity timestamps. The operators and ranges pick an edge of that
// atom, which is what makes an exclusive bound fall out without a flag.
func tokenEdges(tok string) (start, end time.Time, err error) {
	if t, e := time.Parse("2006-01-02", tok); e == nil {
		start = t.UTC()
		return start, start.AddDate(0, 0, 1), nil
	}
	if t, e := time.Parse(time.RFC3339, tok); e == nil {
		start = t.UTC()
		return start, start.Add(time.Second), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("not a gh date token: %q", tok)
}

func invalidCreated(s string) error {
	return fmt.Errorf("invalid created date %q: want a gh date such as 2024-01-15, >=2024-01-15, or 2024-01-01..2024-02-01", s)
}
