package settings_test

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/palette"
)

// AC9 is asserted here rather than by a golden, and ADR-0013 owns the reason: lipgloss
// renders truecolour whatever the environment says, so the frame behind a NO_COLOR run is
// byte-identical to the frame behind a coloured one. The claim is about what reaches the
// terminal, so it is asserted over an explicit colorprofile.Writer at the profile R15a's
// resolver returns. This is the only place in the suite that renders through a profile.

// sgr matches an SGR sequence and captures its parameters.
var sgr = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

// colourParams returns the SGR parameters in s that set a colour: 30 to 49, which covers
// the eight foreground and background colours and both extended (38 and 48) introducers,
// plus the bright aixterm ranges. Text decoration is not among them, deliberately.
func colourParams(s string) []int {
	var found []int
	for _, m := range sgr.FindAllStringSubmatch(s, -1) {
		for _, p := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }) {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			if (n >= 30 && n <= 49) || (n >= 90 && n <= 97) || (n >= 100 && n <= 107) {
				found = append(found, n)
			}
		}
	}
	return found
}

// stripSGR removes every SGR sequence, leaving the text a monochrome terminal would show.
func stripSGR(s string) string { return sgr.ReplaceAllString(s, "") }

// TestNoColorFrameCarriesNoColour pins settings AC9 end to end: with NO_COLOR set to any
// value, R15a's resolver caps the profile at Ascii, and the Settings frame written through
// a colorprofile.Writer at that profile reaches the terminal with no colour sequence in it,
// whichever palette the theme selected. The frame itself is unchanged, which is the whole
// reason this is not a golden.
func TestNoColorFrameCarriesNoColour(t *testing.T) {
	for _, value := range []string{"", "1", "yes", "0"} {
		for _, theme := range config.Themes() {
			restore := palette.Use(palette.ResolveAppearance(theme, true))
			frame := open(&recorder{}).View()
			restore()
			if len(colourParams(frame)) == 0 {
				t.Fatal("precondition: the rendered frame carries no colour at all, so this proves nothing")
			}

			env := func(key string) (string, bool) {
				switch key {
				case "NO_COLOR":
					return value, true
				case "CLICOLOR_FORCE": // the case the pinned library gets wrong, on a piped stream
					return "1", true
				}
				return "", false
			}
			profile := palette.ColorProfile(env, colorprofile.TrueColor)
			if profile > colorprofile.Ascii {
				t.Fatalf("NO_COLOR=%q resolved to %v, want no higher than Ascii", value, profile)
			}

			var buf bytes.Buffer
			w := &colorprofile.Writer{Forward: &buf, Profile: profile}
			if _, err := w.WriteString(frame); err != nil {
				t.Fatalf("write frame: %v", err)
			}
			if got := colourParams(buf.String()); len(got) != 0 {
				t.Errorf("NO_COLOR=%q with theme %q emitted colour parameters %v (AC9)", value, theme, got)
			}
			if !strings.Contains(buf.String(), "\x1b[1m") {
				t.Errorf("NO_COLOR=%q dropped bold; Ascii suppresses colour and keeps text decoration (AC9)", value)
			}
			if !strings.Contains(buf.String(), "Theme") {
				t.Errorf("the theme row did not survive the profile writer")
			}
		}
	}
}

// TestThemeChangesTheRenderedFrame pins settings R6 where it matters: the theme is a
// rendering decision, so the light palette paints different bytes from the dark one. Without
// this the setting would be a word in a config file and a row in a view, which is the
// do-nothing switch R11's deferral note refuses.
func TestThemeChangesTheRenderedFrame(t *testing.T) {
	restore := palette.Use(palette.ResolveAppearance(config.ThemeDark, false))
	dark := open(&recorder{}).View()
	restore()

	restore = palette.Use(palette.ResolveAppearance(config.ThemeLight, true))
	light := open(&recorder{}).View()
	restore()

	if dark == light {
		t.Fatal("the dark and light themes rendered identical frames; the theme setting is inert (R6)")
	}
	// R16: meaning never rides on colour, so the two frames carry the same text and the
	// same shape, and differ in colour alone.
	if stripSGR(dark) != stripSGR(light) {
		t.Errorf("the palettes changed the text, not just the colour:\n%s\n%s", stripSGR(dark), stripSGR(light))
	}
}
