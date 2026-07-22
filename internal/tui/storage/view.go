package storage

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Column geometry for the merged list (R4). Four columns are fixed; NAME flexes above a
// floor. lead carries the cursor and selection markers. DETAIL holds a Cache's
// last_accessed (R7), a live Artifact's expires_at (R12), or a Tombstone's reclaims-nothing
// statement (R9), each of which fits.
const (
	kindW    = 8  // ARTIFACT, the longer kind
	sizeW    = 11 // right-aligned, wide enough for "10.59 GB" and a comma group
	repoW    = 20 // owner/name, matching the Feed's repository floor
	nameMin  = 12
	detailW  = 27 // "tombstone, reclaims nothing"
	colSep   = "  "
	minWidth = 92 // lead 6 + the fixed columns, their separators and NAME's floor
	trunc    = "…"
	dateFmt  = "2006-01-02"
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden over
// View() is byte-stable on any machine (ADR-0013).
var (
	styleTitle     = lipgloss.NewStyle().Bold(true)
	styleHeader    = lipgloss.NewStyle().Bold(true)
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleTombstone = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a")) // a Tombstone reclaims nothing (R9)
	styleCache     = lipgloss.NewStyle().Foreground(lipgloss.Color("#5fafff"))
	styleArtifact  = lipgloss.NewStyle().Foreground(lipgloss.Color("#87d787"))
	styleSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaf00"))
	styleWarn      = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff875f"))
	styleArchived  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))
)

// View renders the tab from held state alone, with no live terminal and no network (R25).
// While the confirmation modal is open it paints that in place of the list, led by the
// frozen set's reclaimable-bytes figure so an expired Artifact confirms at zero bytes
// (R11, AC8). The modal itself is the shared pane, unchanged (R17).
func (m Model) View() string {
	if m.confirmOpen {
		return m.confirmView()
	}
	if len(m.storage) == 0 {
		return styleDim.Render("Storage — press r to load Cache and Artifact usage across your repositories.")
	}
	var lines []string
	lines = append(lines, m.summaryLine())
	if rollup := m.rollupLines(); len(rollup) > 0 {
		lines = append(lines, "")
		lines = append(lines, rollup...)
	}
	lines = append(lines, "")
	lines = append(lines, m.listLines()...)
	if hint := m.hintLine(); hint != "" {
		lines = append(lines, "")
		lines = append(lines, styleDim.Render(hint))
	}
	return strings.Join(lines, "\n")
}

// confirmView composes the tab's reclaimable-bytes figure over the shared confirmation
// modal (R11, R17). The modal shows the count and breakdown unchanged; the byte figure is
// the tab's, because the merged list is where bytes have meaning and R11's zero for a
// Tombstone is a storage fact the shared pane does not carry.
func (m Model) confirmView() string {
	reclaim := styleTitle.Render("Reclaims "+formatBytes(m.pendingReclaim)) + "."
	return reclaim + "\n\n" + m.confirm.View()
}

// summaryLine states the scope and the grand totals: the Cache bytes and count from R1's
// per-repository figures summed (never from an enumerated list), and the Artifact bytes
// that are actually reclaimable with the Tombstone count called out, because a naive sum
// of size_in_bytes over Artifacts is wrong by the Tombstones (R1, R10).
func (m Model) summaryLine() string {
	cacheBytes, cacheCount, artifactReclaim, live, tombstones := m.totals()
	scope := strconv.Itoa(len(m.order)) + " repositories"
	if len(m.order) == 1 {
		scope = textsan.Sanitize(m.order[0].Owner + "/" + m.order[0].Name)
	}
	parts := []string{
		styleTitle.Render("Storage") + " " + styleDim.Render(scope),
		"Caches " + styleCache.Render(formatBytes(cacheBytes)) + styleDim.Render(" ("+strconv.Itoa(cacheCount)+")"),
		"Artifacts " + styleArtifact.Render(formatBytes(artifactReclaim)) + styleDim.Render(" reclaimable, "+strconv.Itoa(live)+" live"),
	}
	if tombstones > 0 {
		parts = append(parts, styleTombstone.Render(plural(tombstones, "tombstone")))
	}
	line := strings.Join(parts, styleDim.Render("   "))
	if label := m.incompleteLabel(); label != "" {
		line += "\n" + styleWarn.Render(label)
	}
	return line
}

// totals is the grand rollup: R1's Cache figures summed across the in-scope repositories,
// the reclaimable Artifact bytes (Tombstones excluded, R10), and the live and Tombstone
// counts.
func (m Model) totals() (cacheBytes int64, cacheCount int, artifactReclaim int64, live, tombstones int) {
	for _, id := range m.order {
		st := m.storage[id.String()]
		cacheBytes += st.ActiveCachesSizeInBytes
		cacheCount += st.ActiveCachesCount
		for _, a := range st.Artifacts {
			if a.Tombstone() {
				tombstones++
				continue
			}
			live++
			artifactReclaim += a.ReclaimableBytes()
		}
	}
	return cacheBytes, cacheCount, artifactReclaim, live, tombstones
}

// incompleteLabel names an in-scope repository whose Cache or Artifact enumeration did not
// account for its totals, so the list is honest that it is not the whole story while R1's
// figures still stand (R2, R3, AC2).
func (m Model) incompleteLabel() string {
	var caches, artifacts int
	for _, id := range m.order {
		st := m.storage[id.String()]
		if !st.cacheListComplete() {
			caches++
		}
		if !st.ArtifactsComplete {
			artifacts++
		}
	}
	switch {
	case caches > 0 && artifacts > 0:
		return "Cache and Artifact lists incomplete in " + strconv.Itoa(caches) + " and " + strconv.Itoa(artifacts) + " repos; the totals above are the endpoint's own figures"
	case caches > 0:
		return "Cache list incomplete in " + strconv.Itoa(caches) + " repos; the total above is the endpoint's own figure (R2)"
	case artifacts > 0:
		return "Artifact total is an estimate in " + strconv.Itoa(artifacts) + " repos; enumeration did not complete (R3)"
	default:
		return ""
	}
}

// rollupLines is R0's per-repository rollup, led by the repositories hoarding the most
// Caches, because "which of my repositories is hoarding Caches?" is the question this view
// exists to answer and a merged list cannot answer it (R0, R1). It appears only under a
// multi-repository scope; a single repository is its own rollup.
func (m Model) rollupLines() []string {
	if len(m.order) <= 1 {
		return nil
	}
	type rr struct {
		repo       domain.RepoID
		cacheBytes int64
		cacheCount int
		artifact   int64
		gate       string
	}
	rows := make([]rr, 0, len(m.order))
	for _, id := range m.order {
		st := m.storage[id.String()]
		var art int64
		for _, a := range st.Artifacts {
			art += a.ReclaimableBytes()
		}
		rows = append(rows, rr{id, st.ActiveCachesSizeInBytes, st.ActiveCachesCount, art, m.gateLabel(id)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].cacheBytes > rows[j].cacheBytes })

	out := []string{styleHeader.Render("  " + pad("REPOSITORY", repoW) + colSep + rpad("CACHES", 16) + colSep + "ARTIFACTS")}
	for _, r := range rows {
		repo := textsan.Sanitize(r.repo.Owner + "/" + r.repo.Name)
		caches := formatBytes(r.cacheBytes) + " (" + strconv.Itoa(r.cacheCount) + ")"
		line := "  " + pad(repo, repoW) + colSep + rpad(caches, 16) + colSep + formatBytes(r.artifact)
		if r.gate != "" {
			line += "  " + r.gate
		}
		out = append(out, line)
	}
	return out
}

// gateLabel names why a repository cannot be reclaimed, distinguishing archived (permanent)
// from merely read-only (might change), because saying an archived repository's storage can
// never be reclaimed is not the same as reporting a permission (R20).
func (m Model) gateLabel(id domain.RepoID) string {
	r, ok := m.capability[id.String()]
	if !ok {
		return ""
	}
	if r.Archived {
		return styleArchived.Render("archived, never reclaimable")
	}
	if !r.Permissions.Push {
		return styleDim.Render("read-only")
	}
	return ""
}

// listLines is the merged Cache-and-Artifact list (R4), a header and the windowed rows.
func (m Model) listLines() []string {
	rows := m.displayRows()
	out := []string{m.listHeader()}
	if len(rows) == 0 {
		out = append(out, styleDim.Render("  no Caches or Artifacts in scope"))
		return out
	}
	capacity := m.listCapacity()
	end := m.top + capacity
	if end > len(rows) {
		end = len(rows)
	}
	for i := m.top; i < end; i++ {
		out = append(out, m.listRow(rows[i], i == m.cursor))
	}
	return out
}

// listHeader labels the merged list's columns.
func (m Model) listHeader() string {
	cells := []string{
		pad("KIND", kindW),
		lpad("SIZE", sizeW),
		pad("REPOSITORY", repoW),
		pad("NAME", m.nameWidth()),
		"DETAIL",
	}
	return styleHeader.Render("      " + strings.Join(cells, colSep))
}

// listRow renders one merged-list row: its kind (R5), its size in units suited to its own
// magnitude so a small row never rounds to zero (R6), the owning repository, its Cache key
// or Artifact name (sanitised), and a per-kind DETAIL. A Cache shows last_accessed_at (R7);
// a live Artifact shows expires_at (R12); a Tombstone states that deleting it reclaims
// nothing (R9).
func (m Model) listRow(r storeRow, cursor bool) string {
	lead := "  "
	if cursor {
		lead = styleSelected.Render("> ")
	}
	box := "[ ] "
	if m.selected[r.key()] {
		box = styleSelected.Render("[x] ")
	}

	var kindLabel, name, detail string
	var kindStyle lipgloss.Style
	switch r.kind {
	case ops.KindCache:
		kindStyle = styleCache
		kindLabel = "CACHE"
		name = r.cache.Key
		detail = "accessed " + r.cache.LastAccessedAt.UTC().Format(dateFmt) // R7
	case ops.KindArtifact:
		kindStyle = styleArtifact
		kindLabel = "ARTIFACT"
		name = r.artifact.Name
		if r.tombstone() {
			kindStyle = styleTombstone
			detail = styleTombstone.Render("tombstone, reclaims nothing") // R9
		} else {
			detail = "expires " + r.artifact.ExpiresAt.UTC().Format(dateFmt) // R12
		}
	}

	repo := textsan.Sanitize(r.repo.Owner + "/" + r.repo.Name)
	cells := []string{
		kindStyle.Render(pad(kindLabel, kindW)),
		lpad(formatBytes(r.size()), sizeW),
		pad(repo, repoW),
		pad(textsan.Sanitize(name), m.nameWidth()),
		detail,
	}
	return lead + box + strings.Join(cells, colSep)
}

// hintLine names the keys the tab acts on, drawn from the registry so it advertises exactly
// what it matches (R7a, AC18).
func (m Model) hintLine() string {
	sel := strconv.Itoa(len(m.selected))
	filter := "a artifacts-only"
	if m.artifactsOnly {
		filter = "a all"
	}
	return "  " + m.profile.ToggleSelect.Help().Key + " select (" + sel + ")   " +
		m.profile.Delete.Help().Key + " reclaim   " + filter + "   " +
		m.profile.Refresh.Help().Key + " refresh"
}

// nameWidth is the flex NAME column: the width less the fixed columns, their separators and
// the DETAIL column, floored so a narrow terminal still lays out.
func (m Model) nameWidth() int {
	fixed := 6 + kindW + sizeW + repoW + detailW + 4*len(colSep)
	w := m.contentWidth() - fixed
	if w < nameMin {
		return nameMin
	}
	return w
}

func (m Model) contentWidth() int {
	if m.width < minWidth {
		return minWidth
	}
	return m.width
}

// listCapacity is the number of merged-list rows the viewport shows, the height less the
// summary, the rollup, the list header and the hint. It floors at one so a tiny terminal
// still pages.
func (m Model) listCapacity() int {
	chrome := 2 + m.rollupChrome() + 2 // summary+blank, rollup, list header, hint(+blank)
	n := m.height - chrome
	if n < 1 {
		return 1
	}
	return n
}

// rollupChrome is the line count the rollup section occupies, zero under a single-repository
// scope.
func (m Model) rollupChrome() int {
	if len(m.order) <= 1 {
		return 0
	}
	return len(m.order) + 2 // blank, header, one row per repository
}

// formatBytes renders a byte count in decimal units suited to its magnitude, so a list
// spanning 302,460,229 and 145,212 bytes shows both non-zero (R6, AC6). The units are the
// canon's: 10,587,236,096 bytes reads 10.59 GB, matching the measured figure. Bytes below a
// kilobyte are exact; above it two decimals keep a small row from rounding to zero.
func formatBytes(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1_000_000:
		return fmt.Sprintf("%.2f KB", float64(n)/1e3)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.2f MB", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	}
}

// pad right-pads or truncates s to exactly w columns; lpad left-pads (right-aligns) a value
// that already fits; rpad right-pads without truncating. Widths are rune counts, which equal
// display width for the ASCII the sizes, ids, dates and keys use.
func pad(s string, w int) string {
	r := []rune(s)
	switch {
	case len(r) > w:
		if w <= 1 {
			return trunc
		}
		return string(r[:w-1]) + trunc
	case len(r) < w:
		return s + strings.Repeat(" ", w-len(r))
	default:
		return s
	}
}

func lpad(s string, w int) string {
	if len([]rune(s)) >= w {
		return pad(s, w)
	}
	return strings.Repeat(" ", w-len([]rune(s))) + s
}

func rpad(s string, w int) string {
	if len([]rune(s)) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len([]rune(s)))
}

// plural renders a count with its noun, adding an s for anything but one.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
