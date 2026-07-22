package governor_test

import (
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
)

// openCassette opens a recorded fixture in replay-only mode with the v4 default
// matcher, which compares the full header map (ADR-0013): the conditional
// request's If-None-Match is part of the match, so nothing passes vacuously. The
// two headers go-gh injects that vary per machine are ignored by canonical name,
// as the store's own cassette test does. WithReplayableInteractions lets one
// taped interaction answer many identical requests, which is what makes the
// reference-scale Purge (AC3) a small cassette rather than an 18,258-entry one.
func openCassette(t *testing.T, name string) *recorder.Recorder {
	t.Helper()
	rec, err := recorder.New(name,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(cassette.NewDefaultMatcher(
			cassette.WithIgnoreHeaders("User-Agent", "Time-Zone"),
		)),
		recorder.WithReplayableInteractions(true),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", name, err)
		}
	})
	return rec
}
