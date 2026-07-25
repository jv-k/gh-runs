package settings_test

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/jv-k/gh-runs/v2/internal/config"
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

// TestNoColorFrameCarriesNoColour pins settings AC9 end to end: with NO_COLOR set to any
// value, R15a's resolver caps the profile at Ascii whatever the theme says, and the
// Settings frame written through a colorprofile.Writer at that profile reaches the terminal
// with no colour sequence in it. The frame itself is unchanged, which is the whole reason
// this is not a golden.
func TestNoColorFrameCarriesNoColour(t *testing.T) {
	frame := open(&recorder{}).View()
	if len(colourParams(frame)) == 0 {
		t.Fatal("precondition: the rendered frame carries no colour at all, so this proves nothing")
	}

	for _, value := range []string{"", "1", "yes", "0"} {
		for _, theme := range config.Themes() {
			env := func(key string) (string, bool) {
				switch key {
				case "NO_COLOR":
					return value, true
				case "CLICOLOR_FORCE": // the case the pinned library gets wrong, on a piped stream
					return "1", true
				}
				return "", false
			}
			profile := config.ColorProfile(env, theme, colorprofile.TrueColor)
			if profile > colorprofile.Ascii {
				t.Fatalf("NO_COLOR=%q with theme %q resolved to %v, want no higher than Ascii", value, theme, profile)
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
