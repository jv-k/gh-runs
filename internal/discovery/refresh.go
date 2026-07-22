package discovery

import (
	"context"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// DueForReprobe returns the repositories due for a conditional re-probe at now,
// under the two-tier cadence R11 and ADR-0020 fix. A repository holding a persisted
// ETag re-probes on the fast interval (Options.Refresh, config's
// discovery_refresh_minutes, default 5 minutes); one without re-probes on the fixed
// hourly constant no setting alters. The split is what bounds the expensive case at
// ~163 unconditional requests per hour whatever the fast interval is set to: a
// repository lands in a tier by the response it last gave, so the design is
// self-adapting to the still-open question of whether an empty Run list carries an
// ETag.
//
// Timing comes from the injected clock the caller passes as now, so a test of the
// cadence advances virtual time and completes without sleeping (R21, AC15). A
// repository never probed this session is due at once: it needs its first
// revalidation, which a persisted ETag makes free (R12).
func (d *Discovery) DueForReprobe(now time.Time) []domain.RepoID {
	d.mu.Lock()
	defer d.mu.Unlock()

	var due []domain.RepoID
	for key, r := range d.records {
		last, probed := d.probed[key]
		if !probed {
			due = append(due, r.ID())
			continue
		}
		interval := d.intervalLocked(key)
		if !now.Before(last.Add(interval)) {
			due = append(due, r.ID())
		}
	}
	return due
}

// intervalLocked is a repository's re-probe interval: the fast tier when its last
// probe carried an ETag, the hourly constant otherwise (R11, R12, ADR-0020). A
// non-positive Options.Refresh falls back to the hourly constant rather than
// re-probing continuously, so a misconfigured interval degrades safely; config
// clamps the real value to a one-minute floor before it ever reaches here.
func (d *Discovery) intervalLocked(key string) time.Duration {
	if d.etagged[key] {
		if d.opts.Refresh > 0 {
			return d.opts.Refresh
		}
	}
	return hourlyTier
}

// Reprobe re-probes the given repositories conditionally and folds each result
// back into the set as it returns, then persists (R11, R12). The requests carry
// the ETags the store persisted from the previous probe, so an unchanged
// repository answers 304, costs no primary allowance, and keeps its poll-set
// membership; a repository that acquired its first Run breaks its own ETag,
// answers 200, and enters the poll set (R12, AC10). It reuses the pass's fan-out,
// so a re-probe rides the same transport limiter that bounds the wire at 10
// (R16, AC13, ADR-0018). A caller drives the cadence by passing DueForReprobe's
// result; an on-demand refresh passes the whole set, or runs a full Pass to
// re-enumerate.
func (d *Discovery) Reprobe(ctx context.Context, ids []domain.RepoID, emit func(Record)) {
	if len(ids) == 0 {
		return
	}

	// Carry each repository's recorded capability forward: a re-probe learns only
	// the classification, exactly as adoption does, because capability rode along
	// with enumeration and no re-probe re-reads it (R7).
	d.mu.Lock()
	caps := make(map[string]Record, len(ids))
	for _, id := range ids {
		if r, ok := d.records[id.String()]; ok {
			caps[id.String()] = r
		}
	}
	d.mu.Unlock()

	d.fanOut(ctx, ids, func(res probeResult) Record {
		prev, ok := caps[res.id.String()]
		if !ok {
			prev = Record{Host: res.id.Host, Owner: res.id.Owner, Name: res.id.Name}
		}
		prev.HasRuns = res.hasRuns
		return prev
	}, emit)

	d.persist()
}
