package palette_test

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/palette"
)

// TestAppearanceFollowsTheTheme pins settings R6: auto derives the palette from the terminal
// background as gh does, and dark and light state it outright and ignore what the terminal
// reported.
func TestAppearanceFollowsTheTheme(t *testing.T) {
	for _, tc := range []struct {
		theme        config.Theme
		terminalDark bool
		want         palette.Appearance
	}{
		{config.ThemeAuto, true, palette.Dark},
		{config.ThemeAuto, false, palette.Light},
		{config.ThemeDark, false, palette.Dark},
		{config.ThemeDark, true, palette.Dark},
		{config.ThemeLight, true, palette.Light},
		{config.ThemeLight, false, palette.Light},
	} {
		if got := palette.ResolveAppearance(tc.theme, tc.terminalDark); got != tc.want {
			t.Errorf("ResolveAppearance(%q, terminalDark=%v) = %v, want %v", tc.theme, tc.terminalDark, got, tc.want)
		}
	}
	// An unrecognised value cannot reach here through Load, and if one does the default
	// stands rather than a zero palette being painted.
	if got := palette.ResolveAppearance("solarized", true); got != palette.Dark {
		t.Errorf("an unknown theme resolved to %v, want the auto behaviour", got)
	}
}

// TestDefaultAppearanceIsDark pins the value every view holds before anything resolves: the
// dark set, which is what gh falls back to and what every golden in the suite was taken at.
func TestDefaultAppearanceIsDark(t *testing.T) {
	if got := palette.Current(); got != palette.Dark {
		t.Errorf("the ambient appearance starts at %v, want Dark", got)
	}
}

// TestUseSwitchesTheRenderedColour is the whole point of the palette: a style built once
// paints different bytes under the two appearances, so the theme setting is observable in
// the frame rather than only in the config file (settings R6). Use returns the restore, so a
// test never leaves the appearance it set behind.
func TestUseSwitchesTheRenderedColour(t *testing.T) {
	style := lipgloss.NewStyle().Foreground(palette.Muted)

	restore := palette.Use(palette.Dark)
	dark := style.Render("run")
	restore()

	restore = palette.Use(palette.Light)
	light := style.Render("run")
	restore()

	if dark == light {
		t.Fatalf("the light and dark appearances rendered the same bytes (%q); the theme would be inert", dark)
	}
	if !strings.Contains(dark, "run") || !strings.Contains(light, "run") {
		t.Errorf("the appearance swallowed the text: dark=%q light=%q", dark, light)
	}
	if got := palette.Current(); got != palette.Dark {
		t.Errorf("restore left the appearance at %v, want Dark", got)
	}
}

// TestEveryRoleDiffersAcrossAppearances pins that the light set is a real set rather than a
// copy of the dark one with a few entries changed. A role that forgot its light value would
// paint a dark-terminal colour on a white background, which is the failure the theme exists
// to prevent.
func TestEveryRoleDiffersAcrossAppearances(t *testing.T) {
	for name, c := range palette.Roles() {
		style := lipgloss.NewStyle().Foreground(c)

		restore := palette.Use(palette.Dark)
		dark := style.Render("x")
		restore()

		restore = palette.Use(palette.Light)
		light := style.Render("x")
		restore()

		if dark == light {
			t.Errorf("role %q paints the same colour in both appearances", name)
		}
	}
}
