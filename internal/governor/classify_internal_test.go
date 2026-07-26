package governor

import (
	"net/url"
	"testing"
)

// rateLimitPages are the documentation_url values GitHub has been observed or
// documented to send with a rate-limit or abuse 403, including the legacy pages and
// the localised and versioned prefixes it still emits. Open question 1 declines to
// provoke a secondary limit, so this list is drawn from GitHub's own documentation
// rather than from a measurement, and it is deliberately wider than the one page the
// cassettes tape.
var rateLimitPages = []string{
	"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api",
	"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api#about-secondary-rate-limits",
	"https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api",
	"https://docs.github.com/free-pro-team@latest/rest/using-the-rest-api/rate-limits-for-the-rest-api",
	"https://docs.github.com/rest/overview/resources-in-the-rest-api",
	"https://docs.github.com/rest/overview/resources-in-the-rest-api#secondary-rate-limits",
	"https://docs.github.com/free-pro-team@latest/rest/overview/resources-in-the-rest-api#secondary-rate-limits",
	"https://docs.github.com/en/free-pro-team@latest/rest/overview/resources-in-the-rest-api",
	"https://docs.github.com/rest/guides/best-practices-for-integrators",
	"https://docs.github.com/en/rest/guides/best-practices-for-integrators#dealing-with-secondary-rate-limits",
	"https://docs.github.com/rest/using-the-rest-api/best-practices-for-using-the-rest-api",
	"https://docs.github.com/rest/using-the-rest-api/troubleshooting-the-rest-api",
	"https://docs.github.com/en/rest/using-the-rest-api/troubleshooting-the-rest-api#rate-limit-errors",
	"https://docs.github.com/rest/guides/getting-started-with-the-rest-api",
	"https://docs.github.com/rest/overview/api-versions",
	"https://docs.github.com/rest/overview/api-versions#specifying-an-api-version",
}

// TestNoResourceNounMatchesARateLimitPage is the invariant the whole discriminator
// rests on, and it was previously unwritten: no resource noun this classifier can
// resolve may appear in a rate-limit page's path, in either number. A noun that did
// would make a genuine secondary limit read as an authorization outcome, and open
// question 1 is emphatic about which direction that error runs: the governor would
// not back off, would not hold, and would keep issuing into the limit.
//
// It runs the whole known-resource set, singulars included, against every rate-limit
// page above. Every future entry in that set is checked here by construction, which is
// what makes the safety a property rather than a coincidence of GitHub's URL wording.
func TestNoResourceNounMatchesARateLimitPage(t *testing.T) {
	for noun := range knownResources {
		for _, page := range rateLimitPages {
			endpoint := &url.URL{Path: "/repos/o/r/actions/" + noun}
			if docURLPointsAtEndpoint(page, endpoint) {
				t.Errorf("resource %q matches the rate-limit page %s; a secondary limit on that endpoint would read as authorization and the governor would not back off (open question 1)", noun, page)
			}
		}
	}
}

// TestUserDataNeverStandsInForAResource pins the other half: a path segment the user
// controls must never reach the correspondence test as a resource noun. Repository
// names, and the file paths the Dispatch form reads, land in the terminal position of
// several endpoints (GET /repos/{owner}/{repo}, GET /repos/{owner}/{repo}/contents/...),
// where a name like "rest" or "overview" is a substring of a real rate-limit page.
// Only a known resource may match, so a repository can be named anything at all.
func TestUserDataNeverStandsInForAResource(t *testing.T) {
	// Every one of these is a legal repository name and a substring of a rate-limit
	// page's path. A user owning one of them is all it would take.
	hazards := []string{
		"rest", "rate", "limit", "limits", "using", "sing", "best", "guide", "guides",
		"overview", "resource", "resources", "team", "teams", "test", "latest",
		"practices", "versions", "started", "troubleshooting",
	}
	for _, name := range hazards {
		for _, page := range rateLimitPages {
			endpoint := &url.URL{Path: "/repos/someone/" + name}
			if docURLPointsAtEndpoint(page, endpoint) {
				t.Errorf("a repository named %q matches the rate-limit page %s; user data must never stand in for a resource noun (open question 1)", name, page)
			}
		}
	}
}

// TestModernIDsAreSkippedOnEveryBuild pins the resource noun against the size of a real
// GitHub id. Run, Cache and Artifact ids passed 2^31 long ago and are eleven digits
// today, so an id test that parses into a platform-width int fails with ErrRange on a
// 32-bit build, and the id itself is then returned as the resource. Nothing matches it,
// the 403 falls to rate limiting, and every parent-documented endpoint silently reverts
// to costing three backoffs. gh-extension-precompile ships linux-386, windows-386,
// linux-arm and freebsd-386, so those builds are users, not a hypothetical.
//
// This test cannot fail on a 64-bit host, where the platform-width parse succeeds. It is
// the regression pin for the builds where it can, and the parse it guards is fixed at 64
// bits so the noun no longer depends on the word size of the machine.
func TestModernIDsAreSkippedOnEveryBuild(t *testing.T) {
	cases := map[string]string{
		"/repos/o/r/actions/runs/12345678901/cancel": "runs",
		"/repos/o/r/actions/runs/12345678901":        "runs",
		"/repos/o/r/actions/caches/98765432109":      "caches",
		"/repos/o/r/actions/artifacts/45678901234":   "artifacts",
		"/repos/o/r/actions/jobs/56789012345/logs":   "jobs",
	}
	for path, want := range cases {
		if got := terminalResource(path); got != want {
			t.Errorf("terminalResource(%q) = %q, want %q; an id past 2^31 must be skipped on a 32-bit build too", path, got, want)
		}
	}
}

// TestKnownResourcesMatchTheirOwnPage keeps the invariant above from passing
// vacuously: a set that matched nothing would satisfy it and classify every
// authorization 403 as a rate limit. Each noun must still correspond to the reference
// page GitHub documents it on.
func TestKnownResourcesMatchTheirOwnPage(t *testing.T) {
	cases := map[string]string{
		"runs":         "https://docs.github.com/rest/actions/workflow-runs#cancel-a-workflow-run",
		"jobs":         "https://docs.github.com/rest/actions/workflow-jobs#download-job-logs-for-a-workflow-job",
		"workflows":    "https://docs.github.com/rest/actions/workflows#disable-a-workflow",
		"caches":       "https://docs.github.com/rest/actions/cache#delete-a-github-actions-cache-for-a-repository-using-a-cache-id",
		"cache":        "https://docs.github.com/rest/actions/cache#get-github-actions-cache-usage-for-a-repository",
		"artifacts":    "https://docs.github.com/rest/actions/artifacts#delete-an-artifact",
		"permissions":  "https://docs.github.com/rest/actions/permissions#get-github-actions-permissions-for-a-repository",
		"repos":        "https://docs.github.com/rest/repos/repos#list-repositories-for-the-authenticated-user",
		"environments": "https://docs.github.com/rest/deployments/environments#list-environments",
		"contents":     "https://docs.github.com/rest/repos/contents#get-repository-content",
	}
	for noun, page := range cases {
		if !knownResources[noun] {
			t.Errorf("%q is exercised here but is not a known resource; the two lists must agree", noun)
			continue
		}
		endpoint := &url.URL{Path: "/repos/o/r/actions/" + noun}
		if !docURLPointsAtEndpoint(page, endpoint) {
			t.Errorf("resource %q does not correspond to its own reference page %s; an authorization 403 there would read as a rate limit and spend R19a's backoffs", noun, page)
		}
	}
	for noun := range knownResources {
		if _, ok := cases[noun]; !ok {
			t.Errorf("known resource %q has no reference page exercised here; every entry must be shown to correspond to something", noun)
		}
	}
}
