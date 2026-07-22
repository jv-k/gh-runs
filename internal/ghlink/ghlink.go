// Package ghlink parses GitHub's Link header, returning the rel="next" URL that
// paginates a listing (ADR-0005). It is a leaf: it imports nothing, so every
// consumer that walks a paginated response depends on it without a cycle.
//
// It exists because three consumers walk the same header. cli's list and
// discovery's enumeration each carried a private copy, and cli's own note named
// the Purge crawl (ops, stage 9) as the third: "a third copy is the trigger to
// promote this angle-bracket walk to an exported helper on a leaf package, rather
// than copy it a third time." This is that helper, and the three now share it.
//
// The parse is bracket-aware rather than a comma split, because a GitHub Actions
// or /user/repos listing URL carries commas of its own in a query
// (affiliation=owner,collaborator,organization_member). A naive split on the
// header's entry separator would tear that query apart and never find the next
// page.
package ghlink

import "strings"

// Next extracts the rel="next" URL from a Link header, or "" when none is
// present. Under an unfiltered listing rel="next" disappears at the true end
// (ADR-0005), so its absence is a crawl's stop condition. The scan walks
// angle-bracket pairs: each link is a <URL> followed by its parameters up to the
// next '<', so the walk reads the parameters between the brackets.
func Next(header string) string {
	for header != "" {
		lt := strings.IndexByte(header, '<')
		if lt < 0 {
			return ""
		}
		gt := strings.IndexByte(header[lt:], '>')
		if gt < 0 {
			return ""
		}
		gt += lt
		url := header[lt+1 : gt]

		rest := header[gt+1:]
		params := rest
		if next := strings.IndexByte(rest, '<'); next >= 0 {
			params = rest[:next]
			header = rest[next:]
		} else {
			header = ""
		}
		if relIsNext(params) {
			return url
		}
	}
	return ""
}

// relIsNext reports whether a link's parameter list declares rel="next",
// tolerating the quoting and spacing GitHub uses. It splits on both attribute
// separators so a stray comma before the next entry does not fold two attributes
// into one.
func relIsNext(params string) bool {
	for _, attr := range strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ',' }) {
		attr = strings.ReplaceAll(attr, "\"", "")
		attr = strings.ReplaceAll(attr, " ", "")
		if attr == "rel=next" {
			return true
		}
	}
	return false
}
