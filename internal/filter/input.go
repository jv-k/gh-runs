package filter

import (
	"slices"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// The filter input's grammar: one line of space-separated tokens, each either an
// axis:value pair or a bare permissive Status or Conclusion value, which is what
// live-run-feed R23's single input accepts.
//
// It lives here, beside the Filter it produces, because it is a second
// representation of the same value and two surfaces speak it: the Feed's filter
// input parses it, and the Workflows tab states a destination in it when it asks
// the root to show one Workflow's Runs (workflow-management R13). A grammar owned
// by a view is a grammar the other caller depends on privately, where renaming a
// token breaks a caller no test covers. Here, ParseQuery and QueryString are one
// pair with a round-trip test over them.
//
// The token spellings are this package's choice, not the canon's: the canon fixes
// the axes and whether each is server-side or client-side (ADR-0016), never the
// input's spelling.

// axisAliases maps every accepted token name to the axis it sets. The canonical
// spelling is the one QueryString emits; the short forms mirror gh's flags.
var axisAliases = map[string]string{
	"branch": "branch", "b": "branch",
	"commit": "commit", "c": "commit",
	"actor": "actor", "user": "actor", "u": "actor",
	"event": "event", "e": "event",
	"workflow": "workflow", "w": "workflow",
	"status": "status", "s": "status", "conclusion": "status",
	"created": "created",
	// R mirrors gh's -R, which is the flag this value is spelled for everywhere else.
	"repo": "repo", "r": "repo",
}

// ParseQuery parses one line of the filter input's grammar into a Filter
// (live-run-feed R22, R23). An axis:value token sets that axis; a bare token, and a
// token whose axis name is not one of ours, is offered to ParseStatus, so a lone
// "failure" filters by Conclusion and an unrecognised value is rejected by name
// (cli-surface R6). The zero Filter comes back from an empty line, which matches
// every Run.
func ParseQuery(s string) (Filter, error) {
	var f Filter
	for _, tok := range strings.Fields(s) {
		name, value, hasColon := strings.Cut(tok, ":")
		axis, known := axisAliases[strings.ToLower(name)]
		if !hasColon || !known {
			if err := f.ParseStatus(tok); err != nil {
				return Filter{}, err
			}
			continue
		}
		switch axis {
		case "branch":
			f.Branch = value
		case "commit":
			f.Commit = value
		case "actor":
			f.Actor = value
		case "event":
			f.Event = value
		case "workflow":
			f.Workflow = value
		case "status":
			if err := f.ParseStatus(value); err != nil {
				return Filter{}, err
			}
		case "created":
			dr, err := ParseCreated(value)
			if err != nil {
				return Filter{}, err
			}
			f.Created = dr
		case "repo":
			// Through the one validation door, the same one the CLI's -R, GH_REPO and
			// settings R7's exclude list use (ADR-0009). A malformed ref or an
			// unsupported host is rejected by name and fails the whole line, because
			// ParseQuery does not adopt a partial filter.
			id, err := domain.ParseRepoRef(value)
			if err != nil {
				return Filter{}, err
			}
			// OR within the axis, and a repeated value does not grow the set, which is
			// the rule ParseStatus already follows for the permissive pair.
			if !slices.Contains(f.Repos, id) {
				f.Repos = append(f.Repos, id)
			}
		}
	}
	return f, nil
}

// QueryString renders a Filter back into the input's grammar, in a fixed axis order
// so the same Filter always renders the same line. It is what a surface handing the
// Feed a filter puts in the operator's input, so the applied filter is a line they
// can read and edit rather than an invisible narrowing.
//
// The repository axis renders as one repo: token per entry, in the bare OWNER/REPO
// spelling. That form round-trips exactly, because NewRepoID rejects every host but
// github.com, so no RepoID in the tree carries a host the two-segment parse could
// lose. It is also the spelling the config writer uses for settings R7's exclude
// list, so the file and the input line agree.
//
// Having a token here is not the same as having a query parameter. The axis still
// has no Query() form and cannot get one, because no such parameter exists
// (ADR-0005, ADR-0016): Match plus this grammar are its whole surface.
//
// The grammar splits on whitespace and has no quoting, in either direction, so a
// value carrying a space is not expressible in it. That is a property of the input
// as it already stands rather than a new limit: a Workflow named "Old Pipeline"
// cannot be typed into it either, which is why a Workflow is addressed here by the
// numeric id its Runs carry.
func (f Filter) QueryString() string {
	var parts []string
	for _, p := range []struct{ axis, value string }{
		{"branch", f.Branch},
		{"commit", f.Commit},
		{"actor", f.Actor},
		{"event", f.Event},
		{"workflow", f.Workflow},
		{"created", f.Created.raw},
	} {
		if p.value != "" {
			parts = append(parts, p.axis+":"+p.value)
		}
	}
	// The permissive pair renders as status: tokens whichever set the value came from,
	// because that is the token ParseStatus classifies and the input accepts for both.
	for _, st := range f.Statuses {
		parts = append(parts, "status:"+string(st))
	}
	for _, cc := range f.Conclusions {
		parts = append(parts, "status:"+string(cc))
	}
	for _, id := range f.Repos {
		parts = append(parts, "repo:"+id.Owner+"/"+id.Name)
	}
	return strings.Join(parts, " ")
}
