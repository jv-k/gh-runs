package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// FastPath resolves the repository the tool was launched inside from the injected
// resolver and yields it first, painted from a single Run-listing request, before
// enumeration or any other repository's probe (R14, R15, AC11). Its capability
// reads not-yet-known until enumeration or adoption records it (R8, AC8): the
// record it admits is Known: false, so a consumer keeps its destructive actions
// disabled and never infers capability from the fact that its Runs listed.
//
// The resolver is main.go's, which wraps go-gh's repository.Current with the
// GH_TOKEN-aware error R14 requires: Current gates its answer on auth.KnownHosts,
// which never reads the keyring, so on a machine without gh it fails with a
// message naming the wrong problem. main.go translates that into the GH_TOKEN
// instruction, and a host other than github.com into an explicit rejection (R18).
// FastPath surfaces whatever the resolver returns; the caller proceeds without a
// fast path rather than failing the run. resolved is false when no resolver is
// configured or none resolved.
func (d *Discovery) FastPath(ctx context.Context, emit func(Record)) (id domain.RepoID, resolved bool, err error) {
	if d.opts.Current == nil {
		return domain.RepoID{}, false, nil
	}
	id, err = d.opts.Current()
	if err != nil {
		return domain.RepoID{}, false, err
	}
	// Re-validate the resolved host at discovery's own boundary, so the AC14 rule
	// that no entry is ever attributed to github.com without resolving there has no
	// hole even if a resolver returned another host (R18). The resolver rejects too,
	// but the check is single-sourced in newRepoID.
	if id, err = newRepoID(id.Host, id.Owner, id.Name); err != nil {
		return domain.RepoID{}, false, err
	}
	// settings R7: an excluded repository receives zero requests, and the terminal
	// happening to sit inside it is not an exception (AC5). It reports unresolved with
	// no error, because there is no fast-path repository for this session and that is a
	// configured choice rather than a failure: an error here would surface R14's
	// GH_TOKEN instruction for a person who has no such problem.
	if d.excluded(id) {
		return domain.RepoID{}, false, nil
	}
	if err := d.classifyOne(ctx, id, emit); err != nil {
		return id, false, err
	}
	return id, true, nil
}

// classifyOne spends one Run listing to record whether a repository has Runs, with its
// capability left not-yet-known (R8, AC8). It is the half of the fast path that follows
// resolution, factored out because adoption needs the same classification for a
// repository the caller resolved elsewhere: R22 admits the launch repository "to the poll
// set if it has Runs", and the poll set is read from that flag.
func (d *Discovery) classifyOne(ctx context.Context, id domain.RepoID, emit func(Record)) error {
	res := d.probe(ctx, id)
	if res.err != nil {
		return res.err
	}
	rec := Record{
		Host:    id.Host,
		Owner:   id.Owner,
		Name:    id.Name,
		HasRuns: res.hasRuns,
		Known:   false, // capability is not known until enumeration or adoption (R8, AC8)
	}
	d.putProbed(rec, d.opts.Clock.Now(), res.hasETag)
	if emit != nil {
		emit(rec)
	}
	return nil
}

// Discover runs a full launch: the fast path first (R14), then the enumerate and
// classify pass (R1, R3), then R22's adoption when the fast-path repository was
// not enumerated. It is the sequence main.go drives on startup. A fast-path
// failure is non-fatal and does not stop the pass, because a session launched
// outside any repository still discovers the account. emit fires once per repository
// as it is classified, the fast-path repository before any pass probe (AC11, AC12).
func (d *Discovery) Discover(ctx context.Context, emit func(Record)) error {
	fastID, resolved, fastErr := d.FastPath(ctx, emit)
	// R14: the fast path is non-fatal, but its error carries the actionable GH_TOKEN
	// instruction, so record it for the caller rather than discarding it. The pass
	// proceeds regardless, because a session launched outside any repository still
	// discovers the account. The CLI surface (stage 6) reads it through FastPathErr.
	d.mu.Lock()
	d.fastPathErr = fastErr
	d.mu.Unlock()

	if err := d.Pass(ctx, emit); err != nil {
		return err
	}

	if resolved {
		// Adoption is a single-request convenience (R22). Its failure leaves the
		// repository painted with its Runs and its capability not-yet-known, which is
		// the safe state: destructive actions stay disabled.
		_ = d.AdoptLaunch(ctx, fastID, emit)
	}
	return nil
}

// AdoptLaunch is R22's session adoption for a launch repository the caller has already
// resolved. When enumeration did not return it, discovery spends one
// GET /repos/{owner}/{repo} to learn its permissions, archived and disabled, and admits
// it for the session.
//
// **It takes the identity, not the resolver, because the composition root already holds
// the answer.** main.go resolves the launch repository once before the engine exists: the
// resolver shells out to git, and one launch needs the answer twice, as the scheduler's
// Options.First and as the host gate. Driving adoption through Discover instead would
// resolve it a second time and re-probe its Run listing, and the root would have to spend
// a whole enumeration pass it may already have loaded from the local-store.
//
// It classifies the repository first when it holds no record for it, which is the case
// whenever the caller is not Discover. R22 admits the repository "to the poll set if it
// has Runs", and that flag comes from a Run listing, not from the capability request. A
// record carrying capability alone would read as having no Runs, and the launch
// repository would drop out of the poll set at the moment the scheduler stopped treating
// it as a special case, which is the opposite of what adoption is for.
//
// The zero identity is a launch outside any git repository, or one whose remote did not
// resolve, and is a no-op rather than an error: there is no repository to adopt, which is
// an ordinary session rather than a failure.
func (d *Discovery) AdoptLaunch(ctx context.Context, id domain.RepoID, emit func(Record)) error {
	if id == (domain.RepoID{}) {
		return nil
	}
	// settings R7, AC5: an excluded repository receives zero requests, and adopt enforces
	// this too. Checking here as well keeps the classification request below from being
	// the hole in that rule.
	if d.excluded(id) {
		return nil
	}
	// A repository enumeration returned is already Known from its payload, so adoption is
	// neither needed nor paid for.
	if d.isKnownMember(id) {
		return nil
	}
	if !d.hasRecord(id) {
		if err := d.classifyOne(ctx, id, emit); err != nil {
			return err
		}
	}
	return d.adopt(ctx, id, emit)
}

// hasRecord reports whether the set holds any record for id, whatever its capability. It
// is the test for "has this repository been classified", distinct from isKnownMember,
// which additionally requires enumeration to have supplied the capability.
func (d *Discovery) hasRecord(id domain.RepoID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.records[id.String()]
	return ok
}

// FastPathErr returns the non-fatal error the most recent Discover's fast path
// produced, or nil. R14's resolver failure (the KnownHosts trap main.go turns into
// the GH_TOKEN instruction, or an unsupported host) does not stop a discovery pass,
// but it carries an actionable message, so Discover records it here rather than
// discarding it. A CLI surface reads it after Discover to show the user the
// instruction while still painting the account it discovered.
func (d *Discovery) FastPathErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fastPathErr
}

// isKnownMember reports whether id is in the set with a capability recorded by
// enumeration (Known and not Adopted). It is the test R22 uses to decide adoption:
// a fast-path repository that enumeration returned is a known member and is not
// adopted; one it did not return is still the not-yet-known placeholder.
func (d *Discovery) isKnownMember(id domain.RepoID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.records[id.String()]
	return ok && r.Known && !r.Adopted
}

// adopt spends R22's one request, GET /repos/{owner}/{repo}, to learn the
// capability of a fast-path repository enumeration did not return, and admits it
// for the session. Its classification (from the fast-path probe) is preserved, its
// capability becomes Known, and it is marked Adopted so Reload does not re-admit it
// in a session launched elsewhere. The record persists so its ETags and capability
// carry across sessions and revalidation stays free (R22).
func (d *Discovery) adopt(ctx context.Context, id domain.RepoID, emit func(Record)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Adoption is the one other request that does not travel through fanOut, so it
	// carries the exclusion check too and the rule has no hole (settings R7, AC5).
	// FastPath already refuses an excluded launch repository, so this is unreachable
	// through Discover; enforcing it here is what keeps that true of any caller.
	if d.excluded(id) {
		return nil
	}
	path := fmt.Sprintf("repos/%s/%s", id.Owner, id.Name)
	resp, err := d.opts.Client.Request(http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("adopt %s: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("adopt %s: status %d", id, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("adopt %s: read body: %w", id, err)
	}
	var repo apiRepo
	if err := json.Unmarshal(body, &repo); err != nil {
		return fmt.Errorf("adopt %s: decode: %w", id, err)
	}

	// Preserve the has-Runs classification the fast-path probe established; adoption
	// learns only the capability (R22).
	hasRuns := false
	d.mu.Lock()
	if existing, ok := d.records[id.String()]; ok {
		hasRuns = existing.HasRuns
	}
	d.mu.Unlock()

	rec := recordFrom(id, repo, hasRuns)
	rec.Adopted = true
	d.put(rec)
	d.persist()
	if emit != nil {
		emit(rec)
	}
	return nil
}
