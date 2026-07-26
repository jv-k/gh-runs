package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// TestLaunchFilterReachesTheFeed pins the wire settings R9 needs at the root: the launch
// filter the config resolved is the filter the Feed opens on. The root already holds the
// resolved Config for the Settings pane, so the value travels through that one field and no
// second precedence is applied here: config.Load has already put the flag above the file
// above the default (R4, AC3).
//
// The assertion is over the painted frame rather than the Feed's private state, because
// that is the property worth holding: a narrowing nobody typed has to be visible.
func TestLaunchFilterReachesTheFeed(t *testing.T) {
	cfg := wiringConfig()
	cfg.LaunchFilter = filter.Filter{
		Branch:      "main",
		Conclusions: []domain.Conclusion{domain.ConclusionFailure},
	}
	m := New(Options{Profile: keys.Standard, Config: cfg})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	view := m.View().Content
	for _, want := range []string{"branch:main", "status:failure"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Feed did not open on the configured launch filter (%q missing):\n%s", want, view)
		}
	}
}

// TestNoLaunchFilterLeavesTheFeedUnfiltered pins the default (R3, AC1): with no launch
// filter configured the Feed states none, exactly as it did before the setting existed.
func TestNoLaunchFilterLeavesTheFeedUnfiltered(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// "edit, then" is the active-filter line's own wording, so the assertion reads that
	// chrome rather than the word "filter", which the help footer also carries.
	if view := m.View().Content; strings.Contains(view, "edit, then") {
		t.Errorf("an unset launch filter still narrowed the Feed:\n%s", view)
	}
}
