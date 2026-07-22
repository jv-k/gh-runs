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
// The half-open atom is the whole of gh's --created grammar, measured read-only
// against cli/cli at stage 6 (cli-list). gh accepts five widths and gh-runs accepts
// the same five: a bare year spans the whole UTC year, a year-month the whole UTC
// month, a full date the whole UTC day, an RFC3339 datetime with a zone one second
// at its instant, and a zone-less datetime one second read as UTC. Each is one atom
// [start, end); the operators and the A..B range pick an edge of it, which is what
// makes an exclusive bound fall out without a flag. The canon pins the field
// (created_at) and the zone (UTC); the atom widths are now measured, not assumed.
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
// accepts gh's five date widths (a bare year, a year-month, a full date, an RFC3339
// datetime with a zone, and a zone-less datetime read as UTC), the comparison
// operators >=, <=, > and < on any of them, and the A..B range with * for an open
// side. It rejects anything else by name, at construction, before any request
// (cli-surface R6). gh's --created is absolute-only, so no clock is consulted.
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

// tokenEdges parses one gh date token into its half-open atom [start, end), in
// UTC. gh accepts five widths (measured read-only against cli/cli), and each maps
// to an atom: a bare year is the whole UTC year, a year-month the whole UTC month,
// a full date the whole UTC day, and either datetime the one second at its stated
// instant, matching the API's second-granularity timestamps. A zone-less datetime
// is read as UTC. The operators and ranges pick an edge of that atom, which is what
// makes an exclusive bound fall out without a flag. The forms are mutually
// exclusive, because time.Parse consumes the whole token, so the order below is for
// reading rather than for correctness, and each layout's own range checks reject a
// non-padded or out-of-range field (2024-1, 2024-13) by name.
func tokenEdges(tok string) (start, end time.Time, err error) {
	if t, e := time.Parse("2006-01-02", tok); e == nil { // full date: the whole UTC day
		start = t.UTC()
		return start, start.AddDate(0, 0, 1), nil
	}
	if t, e := time.Parse(time.RFC3339, tok); e == nil { // datetime with a zone: one second
		start = t.UTC()
		return start, start.Add(time.Second), nil
	}
	if t, e := time.Parse("2006-01-02T15:04:05", tok); e == nil { // zone-less datetime read as UTC: one second
		start = t.UTC()
		return start, start.Add(time.Second), nil
	}
	if t, e := time.Parse("2006-01", tok); e == nil { // year-month: the whole UTC month
		start = t.UTC()
		return start, start.AddDate(0, 1, 0), nil
	}
	if t, e := time.Parse("2006", tok); e == nil { // bare year: the whole UTC year
		start = t.UTC()
		return start, start.AddDate(1, 0, 0), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("not a gh date token: %q", tok)
}

func invalidCreated(s string) error {
	return fmt.Errorf("invalid created date %q: want a gh date such as 2024-01-15, >=2024-01-15, or 2024-01-01..2024-02-01", s)
}
