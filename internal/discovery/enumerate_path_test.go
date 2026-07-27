package discovery

import (
	"net/url"
	"strings"
	"testing"
)

// TestEnumeratePathDoesNotCombineTypeWithAffiliation pins the one parameter rule
// GET /user/repos enforces and no cassette can catch: `type` is mutually exclusive
// with `affiliation` and with `visibility`, and sending it alongside either is a
// 422 on every token, every scope and every account, because it is parameter
// validation and not authorisation. Measured 2026-07-27:
//
//	GET user/repos?per_page=100&affiliation=owner,collaborator,organization_member&type=all
//	  422 {"message":"If you specify visibility or affiliation, you cannot specify type."}
//	GET user/repos?per_page=100&affiliation=owner,collaborator,organization_member
//	  200
//
// This is a guard rather than a fidelity test. A cassette proves what the API said
// to the request we taped, so a cassette whose request was authored rather than
// recorded proves only that we are consistent with ourselves. That is how #154
// survived this package's five fixtures across the seven enumeration requests they
// tape. This test reads the constant the production request is built from and
// applies the API's rule to it directly, so it fails whoever reintroduces the
// combination whatever the fixtures say.
func TestEnumeratePathDoesNotCombineTypeWithAffiliation(t *testing.T) {
	q := enumerateQuery(t)

	if !q.Has("affiliation") {
		t.Fatalf("enumeratePath must name the affiliations explicitly (R1); got %q", enumeratePath)
	}
	// The exclusion is symmetric and `type` is the parameter on one side of it, so
	// testing for `type` alone catches both pairings. Naming `visibility` in the
	// message keeps the rule legible to whoever adds it later.
	if q.Has("type") {
		t.Errorf("enumeratePath sends type=%q alongside affiliation=%q, which GET /user/repos "+
			"rejects with 422 (\"If you specify visibility or affiliation, you cannot specify "+
			"type\"). The same 422 follows a visibility= it is paired with. affiliation carries "+
			"R1's requirement, so type is the redundant one: drop it. Path was %q",
			q.Get("type"), q.Get("affiliation"), enumeratePath)
	}
}

// TestEnumeratePathNamesTheAffiliations holds repo-discovery R1's three
// affiliations against the constant, so dropping `type` cannot quietly narrow the
// result set on the way past. The three are the full set the endpoint offers, which
// is why removing `type=all` changes what nobody sees.
func TestEnumeratePathNamesTheAffiliations(t *testing.T) {
	q := enumerateQuery(t)

	got := strings.Split(q.Get("affiliation"), ",")
	want := []string{"owner", "collaborator", "organization_member"}
	if len(got) != len(want) {
		t.Fatalf("affiliation = %v, want R1's %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("affiliation[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if q.Get("per_page") != "100" {
		t.Errorf("per_page = %q, want 100: R1's two-page reference cost is quoted against "+
			"the maximum page size (ADR-0020)", q.Get("per_page"))
	}
}

// enumerateQuery parses enumeratePath's query. The constant is a path with a query
// glued on rather than a URL, which is exactly what ghclient.Request takes, so it
// is parsed as a relative reference.
func enumerateQuery(t *testing.T) url.Values {
	t.Helper()
	u, err := url.Parse(enumeratePath)
	if err != nil {
		t.Fatalf("enumeratePath %q does not parse: %v", enumeratePath, err)
	}
	return u.Query()
}
