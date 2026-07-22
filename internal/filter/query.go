package filter

import "net/url"

// Query emits the server-side half of a Filter: the query parameters that can be
// pushed without changing the result (ADR-0016). It emits branch, actor, event,
// created and head_sha under their API parameter names, and status under the
// singleton rule below.
//
// It never emits a conclusion parameter, because none exists and the API ignores
// one silently (cli-surface R5), and it never emits the repository axis, which
// has no parameter form, nor workflow, which is an endpoint choice rather than a
// parameter. It does read the Conclusions set, but only to count the pair for the
// status pushdown: a lone Conclusion rides out as status, the one parameter the
// API matches on both fields, never as a conclusion parameter.
//
// The guarantee that Conclusion never reaches the wire is held by the counting
// transport (cli-surface AC4), not by this type. Query simply has no path that
// writes a conclusion parameter.
func (f Filter) Query() url.Values {
	q := url.Values{}
	if f.Branch != "" {
		q.Set("branch", f.Branch)
	}
	if f.Commit != "" {
		q.Set("head_sha", f.Commit)
	}
	if f.Actor != "" {
		q.Set("actor", f.Actor)
	}
	if f.Event != "" {
		q.Set("event", f.Event)
	}
	if !f.Created.empty() {
		q.Set("created", f.Created.raw)
	}
	if v, ok := f.singletonStatus(); ok {
		q.Set("status", v)
	}
	return q
}

// singletonStatus reports the one value across the permissive pair, if the two
// sets hold exactly one between them. The API's status parameter takes one value
// per request, so a pair holding two values (an approvals badge, a multi-select
// input) pushes nothing and rides in Match instead. Pushing one value keeps a
// query never narrower than the Filter, only equal or broader, which is what
// keeps live-run-feed R24's cap label a true upper bound.
func (f Filter) singletonStatus() (string, bool) {
	if len(f.Statuses)+len(f.Conclusions) != 1 {
		return "", false
	}
	if len(f.Statuses) == 1 {
		return string(f.Statuses[0]), true
	}
	return string(f.Conclusions[0]), true
}
