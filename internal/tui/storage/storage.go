// Package storage is the Storage tab (R2). It is built at stage 10
// (storage-reclamation), and this is its stage-7 placeholder: enough of a tab for the
// root's three-tab routing to be real, so the finished tab slots in without a rewrite.
// Like every tab it exposes View() string and an Update the root drives, and is not a
// tea.Model (ADR-0011's tab contract). It accepts the broadcast size and data messages
// and ignores them, so the routing that keeps the Feed alive in the background is
// exercised uniformly here too.
package storage

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Model is the Storage tab's placeholder state: only its size, which it will need when
// the real tab lands.
type Model struct {
	width  int
	height int
}

// New returns the placeholder tab.
func New() Model { return Model{} }

// Update tracks the terminal size and ignores everything else, the minimum a tab does
// while unbuilt.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if s, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = s.Width, s.Height
	}
	return m, nil
}

// View renders the placeholder. The real Storage tab arrives at stage 10.
func (m Model) View() string {
	return lipgloss.NewStyle().Faint(true).Render("Storage — arrives with storage-reclamation (stage 10).")
}
