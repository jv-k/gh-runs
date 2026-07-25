package tui

import (
	"image/color"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/palette"
)

// The root owns the palette because it owns the Settings pane and it is the only model the
// terminal answers (ADR-0011). These pin settings R6's auto member: the palette follows the
// terminal background where the theme says auto, and is fixed where it says dark or light.

// white and black stand in for what a terminal reports for its background.
var (
	white = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	black = color.RGBA{A: 0xff}
)

// themedConfig is a resolved config carrying one theme, for the wiring tests below.
func themedConfig(theme config.Theme) config.Config {
	cfg := wiringConfig()
	cfg.Theme = theme
	return cfg
}

// TestRootAsksTheTerminalForItsBackground pins that auto has something to follow: the root
// asks as it starts, the way Bubble Tea v2 exposes the query, rather than blocking on a
// terminal read before the program is up.
func TestRootAsksTheTerminalForItsBackground(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init issued no command")
	}
	// Init's batch is inspected under a deadline: one of its commands is the coarse tick, and
	// a batch that collapsed to that single command would sleep out the interval here.
	msgs := make(chan tea.Msg, 1)
	go func() { msgs <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-msgs:
	case <-time.After(2 * time.Second):
		t.Fatal("Init returned a single blocking command; the background request is not among its commands (R6)")
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init did not return a batch, got %T", msg)
	}
	// The commands are compared by identity rather than run: one of them is the coarse tick,
	// and running it would sleep out the interval.
	want := reflect.ValueOf(tea.Cmd(tea.RequestBackgroundColor)).Pointer()
	for _, c := range batch {
		if c != nil && reflect.ValueOf(c).Pointer() == want {
			return
		}
	}
	t.Error("Init never asked the terminal for its background; auto has nothing to derive from (R6)")
}

// TestAutoFollowsTheTerminalBackground pins R6's auto member: the terminal answers, and the
// palette follows it, as gh does.
func TestAutoFollowsTheTerminalBackground(t *testing.T) {
	defer palette.Use(palette.Dark)()

	m := New(Options{Profile: keys.Standard, Config: themedConfig(config.ThemeAuto)})
	m = step(t, m, tea.BackgroundColorMsg{Color: white})
	if got := palette.Current(); got != palette.Light {
		t.Errorf("a light terminal background resolved to the %v palette, want light (R6)", got)
	}

	step(t, m, tea.BackgroundColorMsg{Color: black})
	if got := palette.Current(); got != palette.Dark {
		t.Errorf("a dark terminal background resolved to the %v palette, want dark (R6)", got)
	}
}

// TestExplicitThemeIgnoresTheBackground pins the other half of R6: dark and light state the
// palette outright, so what the terminal reports does not move them.
func TestExplicitThemeIgnoresTheBackground(t *testing.T) {
	defer palette.Use(palette.Dark)()

	dark := New(Options{Profile: keys.Standard, Config: themedConfig(config.ThemeDark)})
	step(t, dark, tea.BackgroundColorMsg{Color: white})
	if got := palette.Current(); got != palette.Dark {
		t.Errorf("theme dark on a light terminal resolved to %v, want dark (R6)", got)
	}

	light := New(Options{Profile: keys.Standard, Config: themedConfig(config.ThemeLight)})
	step(t, light, tea.BackgroundColorMsg{Color: black})
	if got := palette.Current(); got != palette.Light {
		t.Errorf("theme light on a dark terminal resolved to %v, want light (R6)", got)
	}
}

// TestThemeChangeAppliesImmediately pins settings R17 for the theme: the running view is the
// authority, so changing the theme in the Settings pane repaints from the next frame rather
// than at the next launch. It is the second setting that applies live, after the keybinding
// profile.
func TestThemeChangeAppliesImmediately(t *testing.T) {
	defer palette.Use(palette.Dark)()

	m := New(Options{Profile: keys.Standard, Config: themedConfig(config.ThemeAuto)})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = step(t, m, tea.BackgroundColorMsg{Color: black}) // a dark terminal: auto is dark
	m = step(t, m, press(","))                           // open Settings

	for i := 0; i < 8 && m.settings.CursorKey() != "theme"; i++ {
		m = step(t, m, press("down"))
	}
	if m.settings.CursorKey() != "theme" {
		t.Fatalf("never reached the theme row; cursor at %q", m.settings.CursorKey())
	}

	m = step(t, m, press("space")) // auto to dark
	if got := palette.Current(); got != palette.Dark {
		t.Errorf("theme dark applied the %v palette, want dark", got)
	}
	m = step(t, m, press("space")) // dark to light
	if got := palette.Current(); got != palette.Light {
		t.Errorf("changing the theme to light did not repaint; the palette is still %v (R17)", got)
	}
	step(t, m, press("space")) // light back to auto, which follows the dark terminal
	if got := palette.Current(); got != palette.Dark {
		t.Errorf("auto did not return to the terminal's own background; the palette is %v (R6)", got)
	}
}
