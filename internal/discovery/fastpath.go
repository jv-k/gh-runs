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
	res := d.probe(ctx, id)
	if res.err != nil {
		return id, false, res.err
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
	return id, true, nil
}

// Discover runs a full launch: the fast path first (R14), then the enumerate and
// classify pass (R1, R3), then R22's adoption when the fast-path repository was
// not enumerated. It is the sequence main.go drives on startup. A fast-path
// failure is non-fatal and does not stop the pass, because a session launched
// outside any repository still discovers the account. emit fires once per repository
// as it is classified, the fast-path repository before any pass probe (AC11, AC12).
func (d *Discovery) Discover(ctx context.Context, emit func(Record)) error {
	fastID, resolved, _ := d.FastPath(ctx, emit)

	if err := d.Pass(ctx, emit); err != nil {
		return err
	}

	// R22: the fast-path repository is adopted for the session only when
	// enumeration did not return it. A repository that enumeration returned is a
	// normal member, already Known from its enumeration payload; the placeholder
	// FastPath admitted has by now been replaced by the enumerated record.
	if resolved && !d.isKnownMember(fastID) {
		if err := d.adopt(ctx, fastID, emit); err != nil {
			// Adoption is a single-request convenience (R22). Its failure leaves the
			// repository painted with its Runs and its capability not-yet-known, which
			// is the safe state: destructive actions stay disabled.
			return nil
		}
	}
	return nil
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
