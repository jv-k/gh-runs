// Package storage is the Storage tab: a single bytes-first view of what is consuming a
// repository's Actions storage, led by Caches, from which Reclamation deletes a Cache or
// an Artifact (storage-reclamation Purpose, R4). It answers "which of my repositories is
// hoarding Caches?" with a per-repository rollup under all-repos (R0, R1), and presents
// the Caches and Artifacts merged into one bytes-descending list (R4) from which a
// selection is deleted through the Purge's graduated confirmation (R17).
//
// A tab is not a tea.Model. It exposes View() string and an Update the root drives, and
// only the root implements tea.Model (ADR-0011's tab contract). The root routes a
// tea.KeyPressMsg here only when this tab is focused, and broadcasts size and data here
// always. This tab may import the confirm pane it opens over its frozen set, which is the
// one import direction the tab contract permits; it never imports another tab (ADR-0011).
//
// It renders to a frame from held state alone, with no live terminal and no network, which
// is what makes R25's goldens cheap. The reclamation view is opened and refreshed
// deliberately rather than polled (ADR-0004, resolved), so its rows are still while the
// cursor is in the list: nothing reorders beneath it between an explicit refresh and its
// result (R19). Reclamation destroys irreversibly, so a deletion travels the single
// mutation entry ops.Execute over the store-governor-limiter chain, paced and logged, and
// the confirmation is reused unchanged (R15, R17, R23).
//
// Downloading an Artifact is the exception to all of that, and takes its own seam for exactly
// that reason (R13). It is a GET that destroys nothing, so it routes through no confirmation,
// writes no deletion-log line and touches no eligibility gate. It is offered on no expired
// Artifact, whose download answers 410 Gone, and a 410 arriving anyway reads as the bytes
// being gone rather than as a transient failure a retry might fix (R14, AC9).
package storage

import (
	"errors"
	"sort"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/confirm"
)

// Planner freezes a selection into an ops.Plan: the shared entry to the confirmation
// chain (ADR-0019). *ops.Ops satisfies it. It is a narrow interface so the tab depends on
// the one call it makes, and a golden test with no planner leaves it nil, where the delete
// key is inert (the destructive action stays disabled until the planner and the discovered
// capability data are wired, repo-discovery R8).
type Planner interface {
	Plan(op ops.Operation, sel []ops.Item, repos map[domain.RepoID]domain.Repo) (ops.Plan, error)
}

// Scope is the set of repositories this view operates over (R0). It is one of two values
// and no others: all-repos, the whole discovered set, and this-repo, the repository of the
// working directory ([settings] R19). The zero value reads as all-repos and New resolves it
// to the constant, so a caller that states no scope gets the default R0 fixes, which is what
// a view whose leading question is "which of my repositories is hoarding Caches?" opens with.
type Scope string

const (
	// ScopeAllRepos fans one cache-usage request out over every discovered repository, over
	// ADR-0003's existing client-side fan-out, and leads with the per-repository rollup. It
	// is the default (R0).
	ScopeAllRepos Scope = "all-repos"
	// ScopeThisRepo presents the working directory's repository alone, the case where you are
	// cleaning one repository deliberately (R0, [settings] R19).
	ScopeThisRepo Scope = "this-repo"
)

// RepoStorage is one repository's storage as the reclamation view holds it: the
// cache-usage endpoint's two exact figures (R1), the enumerated Cache and Artifact lists,
// and whether each enumeration completed (R2, R3). Err records a fetch that failed, which
// R21 treats as an outcome rather than a fault: a 403 can arrive despite push, because a
// fine-grained PAT exposes no scopes.
type RepoStorage struct {
	Repo                    domain.RepoID
	ActiveCachesSizeInBytes int64 // R1: from the cache-usage endpoint, never summed from a list
	ActiveCachesCount       int   // R1: likewise
	Caches                  []domain.Cache
	Artifacts               []domain.Artifact
	ArtifactsComplete       bool // R3: the Artifact enumeration reached the last page
	Err                     error
}

// cacheListComplete reports whether the enumerated Cache list accounts for R1's totals
// (R2). When it does not, the list is labelled incomplete while R1's figures still stand
// as the repository's truth: active_caches_count is the oracle the list is reconciled
// against (R2).
func (s RepoStorage) cacheListComplete() bool {
	if len(s.Caches) < s.ActiveCachesCount {
		return false
	}
	var sum int64
	for _, c := range s.Caches {
		sum += c.SizeInBytes
	}
	return sum >= s.ActiveCachesSizeInBytes
}

// StorageFetched carries one repository's freshly fetched storage, replacing its held
// slice wholesale, exactly as the Feed replaces a repository's Runs on a RunsFetched
// (ADR-0015). Under all-repos the fan-out emits one per repository; a golden test injects
// them directly, with no network (R25).
type StorageFetched RepoStorage

// selKey identifies a selected row. It carries the Kind because a Cache and an Artifact
// can share an id, so keying by id alone would conflate them (storage-reclamation R5, R16;
// purge R4's tuple, widened by Kind for a merged list).
type selKey struct {
	repo domain.RepoID
	kind ops.Kind
	id   int64
}

// storeRow is one row of the merged list: a Cache or an Artifact, with its repository.
// Exactly one of cache and artifact is meaningful, selected by kind.
type storeRow struct {
	repo     domain.RepoID
	kind     ops.Kind
	cache    domain.Cache
	artifact domain.Artifact
}

func (r storeRow) size() int64 {
	if r.kind == ops.KindCache {
		return r.cache.SizeInBytes
	}
	return r.artifact.SizeInBytes
}

func (r storeRow) id() int64 {
	if r.kind == ops.KindCache {
		return r.cache.ID
	}
	return r.artifact.ID
}

func (r storeRow) key() selKey { return selKey{repo: r.repo, kind: r.kind, id: r.id()} }

// tombstone reports whether the row is an expired Artifact, whose deletion reclaims
// nothing (R9).
func (r storeRow) tombstone() bool {
	return r.kind == ops.KindArtifact && r.artifact.Tombstone()
}

// item freezes the row into an ops.Item for a reclamation Plan (R17). It stamps the row's
// owning repository onto the object, which is authoritative because the merged list is
// grouped by it, so the frozen Item resolves to the right DELETE endpoint even if a fetch
// left the object's own Repo unset. The constructor copies the object by value, so the
// frozen set does not follow a later refresh (R5).
func (r storeRow) item() ops.Item {
	if r.kind == ops.KindCache {
		c := r.cache
		c.Repo = r.repo
		return ops.CacheItem(c)
	}
	a := r.artifact
	a.Repo = r.repo
	return ops.ArtifactItem(a)
}

// Options carries the tab's construction seams. main.go fills them: the profile is the
// resolved keybinding set (R7a), Fetch reads one repository's storage over the shared
// client, Repos is the discovered capability data the gate reads (R20) and the fan-out
// covers (R0), Ops freezes a selection into a Plan (R17), and Download writes an Artifact's
// archive to disk (R13). A test may leave any of them nil, where the tab renders held state
// alone: the delete key opens nothing, and the download key neither requests nor advertises
// itself, though it still refuses a Tombstone out loud, because that refusal is a fact about
// the row rather than about the seam (R14).
type Options struct {
	Profile  keys.Profile
	Fetch    Fetch
	Repos    func() []domain.Repo
	Ops      Planner
	Download Downloader

	// Scope is the set of repositories the fan-out covers (R0). The zero value is all-repos,
	// the default R0 fixes. The setting that selects the other is [settings] R19's, which is
	// not built: until it is, main.go states no scope and the tab runs all-repos. The scope is
	// chosen at construction and does not change while running, because changing it also means
	// dropping the held storage: applyFetched accumulates each repository as it arrives, so
	// narrowing the scope would leave the wider one's rows and its rollup on screen.
	Scope Scope
	// CurrentRepo resolves the working directory's repository, which is what this-repo means
	// ([settings] R19). main.go wires it to ghclient.CurrentRepo, the same resolver discovery's
	// fast path takes. It reports false where there is no such repository, and the tab then
	// falls back to all-repos and says so rather than painting an empty view. It is nil in a
	// golden test, which is the same fallback.
	CurrentRepo func() (domain.RepoID, bool)
}

// downloadDoneMsg carries a download's result: the Artifact's name, the path written, or the
// error (R13, R14). It is tagged with the name rather than the id because the line it feeds
// is read by a person, and the name is what they pressed the key over.
type downloadDoneMsg struct {
	name string
	path string
	err  error
}

// Model is the Storage tab's state. The storage map is the truth per repository, keyed by
// RepoID.String() and replaced wholesale as each StorageFetched arrives; order keeps the
// repositories in a stable rollup order. The merged list is computed from held state on
// demand, so a refresh never reorders beneath the cursor between its request and its
// result (R19).
type Model struct {
	width, height int
	active        bool

	profile  keys.Profile
	fetch    Fetch
	repos    func() []domain.Repo
	planner  Planner
	download Downloader

	// scope is R0's two code paths, resolved at construction, and currentRepo is what
	// this-repo resolves to ([settings] R19).
	scope       Scope
	currentRepo func() (domain.RepoID, bool)

	storage map[string]RepoStorage
	order   []domain.RepoID

	// capability is the eligibility gate's data, keyed by RepoID.String(), pulled from the
	// Repos seam on refresh (R20). A repository absent from it makes Plan fail closed
	// (repo-discovery R8, ADR-0019).
	capability map[string]domain.Repo

	cursor int
	top    int

	// selected is keyed by (repo, kind, id) so it survives a refresh and a filter change,
	// and a Cache and an Artifact sharing an id stay distinct (R5, R16).
	selected map[selKey]bool

	// artifactsOnly filters the merged list to Artifacts alone (R8): the bytes-descending
	// sort files the Caches above nearly every Artifact, so this brings the Artifacts within
	// reach.
	artifactsOnly bool

	// confirm is the graduated-confirmation pane, opened over the frozen selection on the
	// delete key (R17, ADR-0011: a tab may import a pane). While it is open it is modal: the
	// tab routes every key to it and paints it in place of the list. planner freezes the
	// selection into the Plan it renders, and pendingReclaim is the frozen set's reclaimable
	// bytes, shown alongside the count so an expired Artifact confirms at zero bytes (R11).
	confirm        confirm.Model
	confirmOpen    bool
	pendingReclaim int64

	// status is a transient line reporting the last download's outcome: the path written
	// (R13), or that the Artifact's bytes are gone (R14, AC9). It is display state alone and
	// nothing reads it back, which keeps a download outside ADR-0006's statelessness rule as
	// well as outside the deletion log, because a download destroys nothing.
	status string
}

// New returns a Storage tab over opts. It holds no storage until the first StorageFetched
// arrives, and paints an empty view until then.
func New(opts Options) Model {
	return Model{
		profile:     opts.Profile,
		fetch:       opts.Fetch,
		repos:       opts.Repos,
		planner:     opts.Ops,
		download:    opts.Download,
		scope:       orAllRepos(opts.Scope),
		currentRepo: opts.CurrentRepo,
		storage:     make(map[string]RepoStorage),
		capability:  make(map[string]domain.Repo),
		selected:    make(map[selKey]bool),
		confirm:     confirm.New(opts.Profile),
	}
}

// orAllRepos resolves the zero Scope to the all-repos default (R0), so the field holds one of
// the two constants and every reader compares against those rather than against "".
func orAllRepos(s Scope) Scope {
	if s == ScopeThisRepo {
		return ScopeThisRepo
	}
	return ScopeAllRepos
}

// SetActive records whether this tab is focused. The reclamation view is opened and
// refreshed deliberately rather than polled (ADR-0004), and the fan-out is one request per
// repository over the discovered account (R0), so gaining focus does not fetch: the user
// presses the refresh key to load, which the empty view names. That keeps a tab switch from
// spending a burst of Budget the operator did not ask for, and keeps SetActive a pure state
// change matching the other tabs (ADR-0011).
// A download's outcome describes a row on this tab, so it does not survive a change of focus
// in either direction (R13): leaving retires it, and arriving does not inherit one. A result
// still in flight when focus changes still lands and still shows, which is deliberate, because
// the alternative is discarding the answer to something the operator asked for. It names the
// Artifact it reports on, so it identifies itself rather than attaching to whatever row the
// cursor now sits on.
func (m Model) SetActive(active bool) Model {
	m.status = ""
	m.active = active
	return m
}

// CapturesInput reports whether this tab holds input focus, which is true while the
// confirmation modal is up (purge R7): a typed count or a y must reach the modal, not the
// root's global keys (ADR-0011, R7).
func (m Model) CapturesInput() bool { return m.confirmOpen }

// Update handles one message the root routed here. Size and data broadcasts reach it
// whether or not it is focused (ADR-0011); a key reaches it only when focused.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.confirm, _ = m.confirm.Update(msg)
		return m, nil

	case StorageFetched:
		m.applyFetched(RepoStorage(msg))
		return m, nil

	case downloadDoneMsg:
		m.status = downloadOutcome(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// downloadOutcome words a download's result (R13, R14, AC9). A 410 is not a failure: the
// Artifact expired, the archive was destroyed by the retention policy, and no retry brings it
// back, so it is worded as the bytes being gone. Anything else is a failure, worded as one,
// because conflating the two would tell an operator to retry what can never succeed or to
// give up on what a second attempt would fix.
func downloadOutcome(msg downloadDoneMsg) string {
	switch {
	case errors.Is(msg.err, ErrArtifactGone):
		return msg.name + " expired: its bytes are gone, and no download can bring them back"
	case msg.err != nil:
		return "Download of " + msg.name + " failed: " + msg.err.Error()
	default:
		return "Downloaded " + msg.name + " to " + msg.path
	}
}

// applyFetched replaces one repository's held storage wholesale and records it in the
// rollup order on first sight (ADR-0015). It also clamps the cursor, because a fetch can
// change how many rows the merged list has.
func (m *Model) applyFetched(rs RepoStorage) {
	key := rs.Repo.String()
	if _, seen := m.storage[key]; !seen {
		m.order = append(m.order, rs.Repo)
	}
	m.storage[key] = rs
	m.clampCursor()
}

// handleKey matches a press against the active profile's registry bindings, never a key
// literal of its own (R7a, AC18). While the confirmation modal is open, every key reaches
// it.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	// A download's outcome reports on the row it was pressed over, so the next keystroke
	// retires it (R13). Left standing it would sit under unrelated rows for the rest of the
	// session and cost the list a row to do it. This precedes the modal branch, so an
	// open-then-abort confirmation does not carry a stale outcome back to the list.
	// startDownload sets it again below, and a result still in flight sets it when it lands,
	// which is the one case where an outcome legitimately outlives the keystroke that asked
	// for it.
	m.status = ""
	if m.confirmOpen {
		return m.handleConfirmKey(k)
	}
	switch {
	case key.Matches(k, m.profile.Delete):
		return m.openConfirm(), nil
	case key.Matches(k, m.profile.ArtifactDownload):
		return m.startDownload() // R13, R14
	case key.Matches(k, m.profile.ArtifactsOnly):
		m.artifactsOnly = !m.artifactsOnly // R8
		m.cursor, m.top = 0, 0
		return m, nil
	case key.Matches(k, m.profile.ToggleSelect):
		m.toggleSelect()
	case key.Matches(k, m.profile.Refresh):
		return m.startFetch() // R19, R24: a refresh is explicit
	case key.Matches(k, m.profile.RowUp):
		m.moveCursor(-1)
	case key.Matches(k, m.profile.RowDown):
		m.moveCursor(1)
	case key.Matches(k, m.profile.PageUp):
		m.moveCursor(-m.pageSize())
	case key.Matches(k, m.profile.PageDown):
		m.moveCursor(m.pageSize())
	case key.Matches(k, m.profile.FirstRow):
		m.cursor = 0
		m.scrollToCursor()
	case key.Matches(k, m.profile.LastRow):
		m.cursor = len(m.displayRows()) - 1
		m.clampCursor()
		m.scrollToCursor()
	}
	return m, nil
}

// startFetch issues one Fetch per in-scope repository, each returning a StorageFetched the
// tab accumulates (R0's fan-out, ADR-0003). With no Fetch wired it is a no-op, which is the
// golden path. The in-scope set is the discovered capability data, which also gates
// eligibility (R20), so the pull refreshes both.
func (m Model) startFetch() (Model, tea.Cmd) {
	if m.repos != nil {
		m.capability = make(map[string]domain.Repo)
		for _, r := range m.repos() {
			m.capability[r.ID.String()] = r
		}
	}
	if m.fetch == nil {
		return m, nil
	}
	scope := m.scopeRepos()
	if len(scope) == 0 {
		return m, nil
	}
	fetch := m.fetch
	cmds := make([]tea.Cmd, 0, len(scope))
	for _, id := range scope {
		id := id
		cmds = append(cmds, func() tea.Msg { return StorageFetched(fetch(id)) })
	}
	return m, tea.Batch(cmds...)
}

// startDownload downloads the Artifact under the cursor to disk (R13). Downloading is a
// genuine non-storage use case, independent of whether the Artifact is worth deleting, so the
// key acts on the row alone and nothing about the reclaimable figures gates it. It runs off
// the Update loop.
//
// The three refusals are ordered deliberately. A Cache row has no archive at all, so the key
// does nothing and says nothing. A Tombstone is refused before the Downloader is consulted,
// and it does say something: its download would answer 410 Gone, so the tab reports the bytes
// as gone rather than issuing a request to be told (R14, AC9). That refusal holds whether or
// not a Downloader is wired, which is why a golden with no seam can still show it. Only a live
// Artifact reaches the nil check, so an unwired tab is inert there and nowhere else.
func (m Model) startDownload() (Model, tea.Cmd) {
	rows := m.displayRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return m, nil
	}
	row := rows[m.cursor]
	if row.kind != ops.KindArtifact {
		return m, nil // only an Artifact has an archive to download (R13)
	}
	a := row.artifact
	a.Repo = row.repo // the merged list is grouped by repository, which is authoritative (R4)
	if a.Tombstone() {
		m.status = downloadOutcome(downloadDoneMsg{name: a.Name, err: ErrArtifactGone}) // R14, AC9
		return m, nil
	}
	if m.download == nil {
		return m, nil
	}
	dl := m.download
	return m, func() tea.Msg {
		path, err := dl(a)
		return downloadDoneMsg{name: a.Name, path: path, err: err}
	}
}

// scopeRepos is the set the fan-out covers, and R0's two code paths are here. Under
// all-repos it is every discovered repository, read from the capability map the refresh
// populated so the fan-out and the gate cover the same set. Under this-repo it is the
// working directory's repository alone, whether or not discovery has reported it:
// this-repo is the repository the operator is standing in, not the intersection of that
// with the enumeration. A repository discovery has not reported keeps its capability
// unknown, so the reclamation gate goes on failing closed over it (R20,
// [repo-discovery] R8).
//
// Where this-repo resolves to nothing it falls back to all-repos, which scopeLabel states
// in the frame rather than leaving silent ([settings] R19).
func (m Model) scopeRepos() []domain.RepoID {
	if id, ok := m.thisRepo(); ok {
		return []domain.RepoID{id}
	}
	out := make([]domain.RepoID, 0, len(m.capability))
	for _, r := range m.capability {
		out = append(out, r.ID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// thisRepo resolves the this-repo scope, and reports false under all-repos and wherever the
// working directory has no repository (which includes no resolver being wired, the golden
// and headless case). Both the fan-out and the frame read it, so the set fetched and the
// scope stated cannot disagree.
func (m Model) thisRepo() (domain.RepoID, bool) {
	if m.scope != ScopeThisRepo || m.currentRepo == nil {
		return domain.RepoID{}, false
	}
	return m.currentRepo()
}

// handleConfirmKey drives the confirmation modal while it is open, routing every key to
// the pane (R7) and acting on its Outcome. An abort dismisses it having issued nothing
// (purge AC6); a confirmation closes it holding the confirmed Plan, and launching Execute
// over it is the running-reclamation surface this stage defers, exactly as the Feed defers
// launching a Purge from a confirmed delete Plan (ADR-0011, ADR-0015).
func (m Model) handleConfirmKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(k)
	switch m.confirm.Outcome() {
	case confirm.Aborted, confirm.Confirmed:
		m.confirm = m.confirm.Close()
		m.confirmOpen = false
	}
	return m, cmd
}

// openConfirm freezes the selection into a delete Plan and opens the confirmation over it
// (R15, R17). The frozen set is the selected rows, or the row under the cursor when nothing
// is selected. It fails closed: with no planner, an empty set, or a repository whose
// capability is not yet known, it stays shut, keeping the destructive action disabled
// (repo-discovery R8, ADR-0019). When every row in the set is ineligible (an archived or
// read-only repository), no modal opens and no request can follow, which is the gate
// distinguishing what can be reclaimed from what cannot (R20, AC13).
func (m Model) openConfirm() Model {
	if m.planner == nil {
		return m
	}
	sel := m.frozenSelection()
	if len(sel) == 0 {
		return m
	}
	plan, err := m.planner.Plan(ops.OpDelete, sel, m.repoSnapshot())
	if err != nil {
		return m // fail closed on an unknown repository (repo-discovery R8)
	}
	if plan.Skipped() == plan.Total() {
		return m // every row ineligible: no delete offered, no request issued (R20, AC13)
	}
	m.pendingReclaim = m.reclaimableOf(sel)
	m.confirm = m.confirm.Open(plan)
	m.confirmOpen = true
	return m
}

// frozenSelection builds the frozen set: a CacheItem or ArtifactItem per selected row in
// display order, or the row under the cursor when the selection is empty. Each constructor
// copies the object, so the set is frozen at this instant (R5).
func (m Model) frozenSelection() []ops.Item {
	rows := m.displayRows()
	if len(m.selected) == 0 {
		if m.cursor >= 0 && m.cursor < len(rows) {
			return []ops.Item{rows[m.cursor].item()}
		}
		return nil
	}
	var items []ops.Item
	for _, r := range rows {
		if m.selected[r.key()] {
			items = append(items, r.item())
		}
	}
	return items
}

// reclaimableOf sums the reclaimable bytes of a frozen set (R10, R11): a Tombstone
// contributes zero, so a set of expired Artifacts confirms at zero bytes (AC8).
func (m Model) reclaimableOf(items []ops.Item) int64 {
	var total int64
	for _, it := range items {
		switch it.Kind {
		case ops.KindCache:
			if it.Cache != nil {
				total += it.Cache.SizeInBytes
			}
		case ops.KindArtifact:
			if it.Artifact != nil {
				total += it.Artifact.ReclaimableBytes()
			}
		}
	}
	return total
}

// repoSnapshot is the eligibility map Plan takes, the discovered capability data keyed by
// RepoID (R20, ADR-0019). A repository absent from it makes Plan fail closed.
func (m Model) repoSnapshot() map[domain.RepoID]domain.Repo {
	out := make(map[domain.RepoID]domain.Repo, len(m.capability))
	for _, r := range m.capability {
		out[r.ID] = r
	}
	return out
}

// toggleSelect toggles the row under the cursor in the selection, keyed by (repo, kind, id)
// so it survives a refresh and a filter change (R5, R16).
func (m *Model) toggleSelect() {
	rows := m.displayRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return
	}
	k := rows[m.cursor].key()
	if m.selected[k] {
		delete(m.selected, k)
	} else {
		m.selected[k] = true
	}
}

// displayRows is the merged Cache-and-Artifact list, sorted by size descending (R4) with a
// deterministic tiebreak so the order is stable across refreshes (R19). artifactsOnly drops
// the Caches (R8). The reclaimable figures and the sort read size_in_bytes, which a
// Tombstone still reports though deleting it recovers nothing, which is exactly why R9's
// tombstone marker and R10's reclaimable exclusion exist.
func (m Model) displayRows() []storeRow {
	var rows []storeRow
	for _, id := range m.order {
		st := m.storage[id.String()]
		if !m.artifactsOnly {
			for _, c := range st.Caches {
				rows = append(rows, storeRow{repo: id, kind: ops.KindCache, cache: c})
			}
		}
		for _, a := range st.Artifacts {
			rows = append(rows, storeRow{repo: id, kind: ops.KindArtifact, artifact: a})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].size() != rows[j].size() {
			return rows[i].size() > rows[j].size() // R4: bytes descending
		}
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		if rows[i].id() != rows[j].id() {
			return rows[i].id() < rows[j].id()
		}
		return rows[i].repo.String() < rows[j].repo.String()
	})
	return rows
}

// moveCursor moves the cursor by delta, clamped, scrolling the viewport.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
	m.scrollToCursor()
}

func (m *Model) clampCursor() {
	n := len(m.displayRows())
	if n == 0 {
		m.cursor, m.top = 0, 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	m.scrollToCursor()
}

func (m *Model) scrollToCursor() {
	rows := m.listCapacity()
	if rows <= 0 {
		m.top = 0
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// pageSize is the number of rows a page-motion key moves.
func (m Model) pageSize() int {
	if n := m.listCapacity(); n > 1 {
		return n
	}
	return 1
}
