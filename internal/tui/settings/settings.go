// Package settings is the Settings pane, a view over the config file stage 0 already
// loads (settings R17, BUILD-ORDER stage 13). It is a pane, not a tab and not a
// tea.Model: it exposes View() string and an Update the root drives, and the root opens
// it over whichever tab is focused because a setting reachable from any tab cannot belong
// to one (ADR-0011's pane contract, R2). It imports config, filter and keys and no tab, and
// nothing routes back to whatever opened it. filter is here for R9's launch filter: the row
// edits it in the grammar that package owns, so this pane spells a filter the way the Feed's
// own input spells one.
//
// It is the view, never the loader: config owns the file's precedence, defaults and
// diagnostics (R3, R4, R14), and this pane edits the resolved Config and writes changed
// keys back through config.Save, which preserves comments, key order and keys this version
// does not recognise (R17, AC11). The only write is that local file write; the pane issues
// no request and touches no API. No secret is written, because config.Save marshals only
// display and behaviour choices and tokens never enter the file (R2).
//
// The 2.0.0 menu carries no notification options. R11 renders notifications R4's events,
// and both defer to 2.1 ([ADR-0013]); a toggle with no subsystem behind it is the
// do-nothing switch notifications R13 refuses, and R18's golden asserts the view never
// renders one.
package settings

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// SaveFunc persists the settings the view changed, writing only the keys that differ
// between prev and next (settings R17, AC11). main.go binds it to config.Save over the
// resolved config path; a test injects a fake that records the write. A nil SaveFunc makes
// the pane edit in memory alone, which is what a headless render test wants.
type SaveFunc func(prev, next config.Config) error

// row identifies one setting the view shows, in the fixed top-to-bottom order the
// constants declare. The order is the view's, and CursorKey exposes each row's config.yml
// key so the root and the tests name a setting rather than an index. None of these rows is
// a rejected setting (R13): the struct config models has no field for one, so none can
// appear here, which is half of what makes R18's absence hold by construction.
type row int

const (
	rowBudget row = iota
	rowProfile
	rowTheme
	rowTimestamp
	rowWorkflowsScope
	rowStorageScope
	rowLaunchFilter
	rowExclude
	rowPin
	rowConfirmThreshold
	rowBreakerFailures
	rowDiscoveryRefresh
	rowCount
)

// Each row's facts, including why it sits where it does, are in the rows table below.

// rowKind is how a row is edited, which decides every gesture the pane offers it: a
// selector cycles on space, and the other three open an editor on enter over a different
// vocabulary each. kindNone is the zero value and belongs to no row, so a row index outside
// the table reports none of the four rather than defaulting into the first.
type rowKind int

const (
	kindNone rowKind = iota
	kindSelector
	kindNumber
	kindList
	kindFilter
)

// rowSpec is everything the pane knows about one row, in one place. Before this, a row's
// facts lived in seven parallel switches that had to stay in the same order with nothing
// enforcing that they did, and adding one setting meant an arm in each.
//
// The table is indexed by the row constant rather than written in order, so a row added in
// the middle of the enum cannot silently take another row's label.
type rowSpec struct {
	kind rowKind
	// key is the config.yml key, the same spelling config.Save writes and Load reads. It is
	// what CursorKey returns.
	key string
	// label is the human name, at the level of intent (Purpose): a person deciding this
	// answers from their own context, without the mechanism behind it.
	label string
	// desc is the one-line help, intent-level and free of the mechanism R13 keeps out of the
	// view. None names a poll interval, a delete rate, a cache TTL, a concurrency level, or a
	// way to skip confirmation.
	desc string
	// options lists the members a selector cycles through, nil for every other kind. It is a
	// function rather than a string so the hint is drawn from the exported set config
	// validates against on each render, which is what keeps the view offering exactly what
	// the loader accepts (R5, R8, R19).
	options func() string
	// bound states a numeric setting's range, so a focused number row shows what a commit
	// will clamp to (R12, R20, R21). Empty for every other kind.
	bound string
	// value renders the row's current value as plain text, before styling.
	value func(m Model) string
	// cycle advances a selector to the next member of its set, nil for every other kind.
	// It takes and returns the Model rather than the value alone because the keybinding row
	// re-seeds the pane's own motion as it cycles.
	cycle func(m Model) Model
	// commit adopts a parsed integer for a number row, clamped to the setting's own bound so
	// the running view holds a value the file would accept (R12, R20, R21). Nil for every
	// other kind. The clamp is config's, so the view and the loader agree on the range by
	// calling one function rather than by two constants matching.
	commit func(m Model, v int) Model
}

// rows is the one table. Adding a setting means an entry here and a registry in config, and
// no switch anywhere.
var rows = [rowCount]rowSpec{
	rowBudget: {
		kind:    kindSelector,
		key:     "budget",
		label:   "Budget",
		desc:    "Share of your API allowance the background refresh may spend.",
		options: func() string { return config.ListSet(config.Tiers(), " / ") },
		value:   func(m Model) string { return string(m.cfg.Budget) },
		cycle: func(m Model) Model {
			m.cfg.Budget = next(m.cfg.Budget, config.Tiers())
			return m
		},
	},
	rowProfile: {
		kind:    kindSelector,
		key:     "keybinding_profile",
		label:   "Keybinding profile",
		desc:    "Motion keys: Vim (k/j) or Standard (arrows).",
		options: func() string { return config.ListSet(config.KeybindingProfiles(), " / ") },
		value:   func(m Model) string { return string(m.cfg.KeybindingProfile) },
		// This is the one cycle that touches more than its own config field. Re-seeding the
		// pane's motion here is what makes Vim's j and Standard's arrows take effect for the
		// pane's very next keystroke. That is this pane applying the change to itself and
		// nothing more: the root reads the profile back off the pane after the key and pushes
		// it to every tab, the running surface and its own global keys (#127).
		cycle: func(m Model) Model {
			m.cfg.KeybindingProfile = next(m.cfg.KeybindingProfile, config.KeybindingProfiles())
			if p, ok := keys.ForName(string(m.cfg.KeybindingProfile)); ok {
				m.profile = p
			}
			return m
		},
	},
	rowTheme: {
		kind:    kindSelector,
		key:     "theme",
		label:   "Theme",
		desc:    "Palette: auto follows your terminal background. NO_COLOR overrides all three.",
		options: func() string { return config.ListSet(config.Themes(), " / ") },
		value:   func(m Model) string { return string(m.cfg.Theme) },
		cycle: func(m Model) Model {
			m.cfg.Theme = next(m.cfg.Theme, config.Themes())
			return m
		},
	},
	// The timestamp format sits with the theme because it is the other purely presentational
	// choice: neither changes what the tool does, both change how a frame reads, and both
	// therefore apply from the next frame. What they do not share is a mechanism. The palette
	// is ambient, so a theme change reaches every view without travelling; the timestamp is a
	// field on one tab, and the root pushes it there. Both are read off this pane by the root
	// after every key it routes here, and this pane pushes neither.
	rowTimestamp: {
		kind:    kindSelector,
		key:     "timestamp",
		label:   "Timestamps",
		desc:    "When a run started: the instant itself, or how long ago it was.",
		options: func() string { return config.ListSet(config.TimestampFormats(), " / ") },
		value:   func(m Model) string { return string(m.cfg.Timestamp) },
		cycle: func(m Model) Model {
			m.cfg.Timestamp = next(m.cfg.Timestamp, config.TimestampFormats())
			return m
		},
	},
	// Both scope rows say "starts" for the reason R9's launch filter does, and it is the
	// requirement rather than a hedge: each tab chooses its scope at construction, because
	// narrowing it also means dropping the held state, so a toggle takes effect at the next
	// launch. The present tense would state something a person would test by pressing refresh.
	rowWorkflowsScope: {
		kind:    kindSelector,
		key:     "workflows_scope",
		label:   "Workflows scope",
		desc:    "The Workflows tab starts covering these repositories.",
		options: func() string { return config.ListSet(config.Scopes(), " / ") },
		value:   func(m Model) string { return string(m.cfg.WorkflowsScope) },
		cycle: func(m Model) Model {
			m.cfg.WorkflowsScope = next(m.cfg.WorkflowsScope, config.Scopes())
			return m
		},
	},
	rowStorageScope: {
		kind:    kindSelector,
		key:     "storage_scope",
		label:   "Storage scope",
		desc:    "The Storage tab starts covering these repositories.",
		options: func() string { return config.ListSet(config.Scopes(), " / ") },
		value:   func(m Model) string { return string(m.cfg.StorageScope) },
		cycle: func(m Model) Model {
			m.cfg.StorageScope = next(m.cfg.StorageScope, config.Scopes())
			return m
		},
	},
	// The launch filter sits with the two tab scopes because it is the third of the same
	// question: which Runs a tab opens on. R19's note says so outright, calling a launch
	// filter pinned to one repository the Runs tab's this-repo scope by another name.
	//
	// Its description says "starts" rather than "now" for the same reason theirs do: R9
	// settles the filter the Feed opens with, and a running Feed keeps whatever filter it is
	// showing until its own / input changes it.
	rowLaunchFilter: {
		kind:  kindFilter,
		key:   "launch_filter",
		label: "Launch filter",
		desc:  "The Runs feed starts filtered by this. Same syntax as its / filter.",
		value: func(m Model) string { return filterLine(m.cfg.LaunchFilter) },
	},
	rowExclude: {
		kind:  kindList,
		key:   "exclude",
		label: "Excluded repositories",
		desc:  "Kept out of discovery and never polled. Naming one with -R still works.",
		value: func(m Model) string { return repoList(m.cfg.Exclude, m.valueRoom()) },
	},
	rowPin: {
		kind:  kindList,
		key:   "pin",
		label: "Pinned repositories",
		// The description states what the code does and no more (#97): the promotion is to
		// the medium tier, which is the cadence an on-screen repository already gets, and
		// exclusion wins because it applies at discovery before any tier is chosen.
		desc:  "Polled as often as an on-screen one, even when scrolled away. Excluding wins.",
		value: func(m Model) string { return repoList(m.cfg.Pin, m.valueRoom()) },
	},
	rowConfirmThreshold: {
		kind:  kindNumber,
		key:   "confirm_threshold",
		label: "Confirmation threshold",
		desc:  "Deletions at or above this many make you type the count.",
		bound: "up to 500, type the count above it",
		value: func(m Model) string { return strconv.Itoa(m.cfg.ConfirmThreshold) },
		commit: func(m Model, v int) Model {
			m.cfg.ConfirmThreshold = config.ClampConfirmThreshold(v)
			return m
		},
	},
	rowBreakerFailures: {
		kind:  kindNumber,
		key:   "purge_breaker_failures",
		label: "Purge breaker threshold",
		desc:  "Consecutive failures before a Purge stops itself.",
		bound: "1 to 500",
		value: func(m Model) string { return strconv.Itoa(m.cfg.BreakerFailures) },
		commit: func(m Model, v int) Model {
			m.cfg.BreakerFailures = config.ClampBreakerFailures(v)
			return m
		},
	},
	rowDiscoveryRefresh: {
		kind:  kindNumber,
		key:   "discovery_refresh_minutes",
		label: "Discovery refresh",
		desc:  "How quickly a newly active repository shows up, in minutes.",
		bound: "1 or more minutes",
		value: func(m Model) string { return strconv.Itoa(m.cfg.DiscoveryRefreshMinutes) + " min" },
		commit: func(m Model, v int) Model {
			m.cfg.DiscoveryRefreshMinutes = config.ClampDiscoveryRefresh(v)
			return m
		},
	},
}

// spec returns the row's entry, or a zero spec for an index outside the table. The zero
// spec's kind is kindNone and its strings are empty, which is what the switches this
// replaced returned from their default arms.
func (r row) spec() rowSpec {
	if r < 0 || r >= rowCount {
		return rowSpec{}
	}
	return rows[r]
}

// isSelector reports whether the row cycles through a small fixed set (space changes it),
// as opposed to a row that opens an editor (enter).
func (r row) isSelector() bool { return r.spec().kind == kindSelector }

// isNumber reports whether the row holds a bounded integer, edited by typing (R12, R20,
// R21), the confirm-pane's typed-count pattern applied to a setting.
func (r row) isNumber() bool { return r.spec().kind == kindNumber }

// isList reports whether the row holds R7's repository list, edited as one line of
// comma-separated OWNER/REPO entries. It is a third edit shape rather than a stretched
// numeric one, but it reuses the same gesture: enter opens, typing rewrites, enter
// commits, esc abandons.
//
// The editor opens pre-filled with the list as it stands, which is what lets one gesture
// both add and remove. A gesture that could only append would be a one-way ratchet, and
// a person who excluded the wrong repository would have to reach for the file anyway.
func (r row) isList() bool { return r.spec().kind == kindList }

// isFilter reports whether the row holds R9's launch filter, edited as one line of the
// filter input's own grammar. It is the same gesture the list row takes, over a different
// vocabulary: enter opens on the filter as it stands, typing rewrites it, enter commits,
// esc abandons.
//
// The grammar is filter's, not this pane's (ParseQuery and QueryString). A second spelling
// here would mean a person who learned the Feed's / filter had to learn another one to
// persist it, and the two would drift the first time an axis was added to either.
func (r row) isFilter() bool { return r.spec().kind == kindFilter }

// isEditable reports whether enter opens an editor on the row, which is true of the
// numeric rows, the list row and the filter row, and of nothing else.
func (r row) isEditable() bool { return r.isNumber() || r.isList() || r.isFilter() }

// configKey is the config.yml key the row maps to, the same spelling config.Save writes
// and Load reads. It is what CursorKey returns.
func (r row) configKey() string { return r.spec().key }

// Model is the pane's state: the keybinding profile it navigates by, the Config it edits,
// the baseline it diffs a save against, the persister, and the transient cursor and edit
// state. It holds no client and issues no request.
type Model struct {
	profile keys.Profile
	cfg     config.Config
	// initial is the last state known to be on disk, the prev config.Save diffs next
	// against so a write touches only the changed key (AC11). It advances only on a
	// successful save, so a failed write is retried by the next edit rather than dropped.
	initial config.Config
	save    SaveFunc

	open   bool
	cursor row
	width  int
	height int

	// editing and editBuf hold an edit in progress (R12, R20, R21 for a number, R7 for
	// the exclude list, R9 for the launch filter). editBuf collects typed text like the
	// confirm pane's typed count, and commit clamps a number to its bound, parses a list
	// into identities, or parses a filter line, before any of them is adopted.
	editing bool
	editBuf string
	// editErr names the entries the last list commit could not parse, so the view reports
	// them rather than dropping them silently. It is the loader's R14 rule applied to the
	// same input arriving by keystroke, and it clears on the next edit.
	editErr string

	// saveErr is the last write's failure, surfaced in the view rather than swallowed so
	// the operator is not misled into believing a change persisted (R17's spirit).
	saveErr error
}

// New returns a closed pane over the resolved config and the persister. The pane holds the
// Config as the authority for the running instance (R17): it edits this copy and writes it
// back, and does not re-read the file while running.
func New(profile keys.Profile, cfg config.Config, save SaveFunc) Model {
	return Model{profile: profile, cfg: cfg, initial: cfg, save: save}
}

// Open shows the pane, resetting the cursor to the top and clearing any edit or error
// state so a reopened pane never carries a stale entry.
func (m Model) Open() Model {
	m.open = true
	m.cursor = 0
	m.editing = false
	m.editBuf = ""
	m.saveErr = nil
	return m
}

// Close hides the pane. The root calls it when the pane signals it is done (esc), and
// returns focus to the tab underneath.
func (m Model) Close() Model {
	m.open = false
	m.editing = false
	m.editBuf = ""
	return m
}

// IsOpen reports whether the pane is showing, which the root reads to paint it and route
// keys to it (ADR-0011).
func (m Model) IsOpen() bool { return m.open }

// Config is the settings as the view currently holds them, the authority for the running
// instance (R17). The root reads it, and a test asserts an edit over it.
func (m Model) Config() config.Config { return m.cfg }

// CursorKey is the config.yml key of the setting under the cursor, exposed so the root can
// label the focused row and a test can name a setting rather than count rows.
func (m Model) CursorKey() string { return m.cursor.configKey() }

// Update handles one message the root routed here. It lays out on size, closes on esc,
// moves the cursor, and edits the focused setting. Every action is matched from the
// keybinding registry with key.Matches, never a key literal of its own (R7a, AC18); the
// digits a numeric edit collects are text input, not a binding, exactly as the confirm
// pane's typed count and the Feed's filter are. The pane's only side effect is the local
// config write, done synchronously because it is a few hundred bytes on a user keystroke,
// not per-frame work; it issues no Cmd.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		if !m.open {
			return m, nil
		}
		if m.editing {
			return m.handleEditKey(msg), nil
		}
		return m.handleNavKey(msg), nil
	}
	return m, nil
}

// handleNavKey drives navigation and the selector rows. esc closes the pane; the motion
// bindings move the cursor and reach both ends; space cycles the selector under the cursor
// (the dispatch pane's choice-cycling key); enter opens the numeric editor on a number
// row. A key the pane does not bind is ignored, so a stray press cannot mutate a setting.
func (m Model) handleNavKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.CloseDetail): // esc: close the pane
		return m.Close()
	// Moving off a row clears the last commit's rejected-entry note, which belonged to
	// that row's edit. Leaving it up while the cursor sits on the Budget row would
	// attach a complaint about a repository name to a setting that has none.
	case key.Matches(k, m.profile.RowUp):
		m.cursor, m.editErr = clampRow(m.cursor-1), ""
	case key.Matches(k, m.profile.RowDown):
		m.cursor, m.editErr = clampRow(m.cursor+1), ""
	case key.Matches(k, m.profile.FirstRow):
		m.cursor, m.editErr = 0, ""
	case key.Matches(k, m.profile.LastRow):
		m.cursor, m.editErr = rowCount-1, ""
	case key.Matches(k, m.profile.ToggleSelect): // space: cycle a selector
		if m.cursor.isSelector() {
			m = m.applyCycle()
			m = m.persist()
		}
	case key.Matches(k, m.profile.OpenDetail): // enter: open the row's editor
		if m.cursor.isEditable() {
			m.editing = true
			// A numeric row starts empty, because typing a threshold replaces it outright
			// and an empty buffer is how "enter, then changed my mind" leaves it alone. A
			// list row and the filter row start on what is there, because editing either is
			// mostly amending one, and starting empty would make removal the only cheap
			// operation.
			m.editBuf = ""
			m.editErr = ""
			switch {
			case m.cursor.isList():
				m.editBuf = strings.Join(repoRefs(m.listFor(m.cursor)), ", ")
			case m.cursor.isFilter():
				m.editBuf = m.cfg.LaunchFilter.QueryString()
			}
		}
	}
	return m
}

// handleEditKey drives an edit in progress (R12, R20, R21 for a number, R7 for the list, R9
// for the launch filter). Typing builds the buffer and backspace trims it, mirroring the
// confirm pane; enter commits, clamping a number to its bound, parsing a list into
// identities, or parsing a filter line; esc cancels, leaving the setting as it was. esc here
// does not close the pane, exactly as the Feed's esc cancels the filter before it closes
// anything.
//
// What counts as typing is the row's, not the pane's: a numeric row takes digits alone, so
// a letter cannot enter a threshold, the list row takes the characters an OWNER/REPO list is
// written in, and the filter row takes printable ASCII, because its values are GitHub's to
// shape. None of the three is a keybinding, exactly as the confirm pane's typed count is not
// (R7a, AC18).
func (m Model) handleEditKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.CloseDetail): // esc: cancel the edit
		return m.endEdit()
	case key.Matches(k, m.profile.OpenDetail): // enter: commit the edit
		switch {
		case m.cursor.isList():
			m = m.commitList()
		case m.cursor.isFilter():
			m = m.commitFilter()
		default:
			m = m.commitNumber()
		}
		editErr := m.editErr
		m = m.endEdit()
		m.editErr = editErr
		return m
	case k.Code == tea.KeyBackspace:
		if n := len(m.editBuf); n > 0 {
			m.editBuf = m.editBuf[:n-1]
		}
	case m.cursor.isList() && listText(k) != "":
		if len(m.editBuf) < listBufMax {
			m.editBuf += listText(k)
		}
	case m.cursor.isFilter() && filterText(k) != "":
		if len(m.editBuf) < filterBufMax {
			m.editBuf += filterText(k)
		}
	case m.cursor.isNumber() && isDigit(k):
		if len(m.editBuf) < 6 { // no setting exceeds three digits; six is slack against a fat finger
			m.editBuf += k.String()
		}
	}
	return m
}

// endEdit leaves edit mode and clears the transient buffer and its error.
func (m Model) endEdit() Model {
	m.editing = false
	m.editBuf = ""
	m.editErr = ""
	return m
}

// listBufMax bounds the exclude editor's buffer. The reference account discovers 163
// repositories, so the ceiling is generous enough that nobody meets it by excluding, and
// low enough that a stuck key cannot grow the buffer without limit.
const listBufMax = 4096

// listFor is the repository list a list row holds. R7 has two, and they are the same
// shape edited by the same gesture over different fields, so the row picks the field
// rather than each call site knowing which list it is looking at.
func (m Model) listFor(r row) []domain.RepoID {
	if r == rowPin {
		return m.cfg.Pin
	}
	return m.cfg.Exclude
}

// commitList parses the edited buffer into the R7 list the cursor is on and adopts it,
// which is the exclude list or the pin list. Entries are
// comma-separated OWNER/REPO, and each goes through domain.ParseRepoRef, the same door the
// loader uses, so the view can never store an identity config.Load would refuse. An entry
// that does not parse is dropped and named in the view rather than silently swallowed,
// which is the loader's rule (R14) applied to the same input arriving by keystroke.
//
// The whole list is replaced, so removing an entry from the buffer removes it from the
// setting. A commit that changes nothing writes nothing, because persist diffs against the
// baseline.
func (m Model) commitList() Model {
	var ids []domain.RepoID
	var rejected []string
	for _, field := range strings.Split(m.editBuf, ",") {
		ref := strings.TrimSpace(field)
		if ref == "" {
			continue
		}
		id, err := domain.ParseRepoRef(ref)
		if err != nil {
			rejected = append(rejected, ref)
			continue
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if m.cursor == rowPin {
		m.cfg.Pin = ids
	} else {
		m.cfg.Exclude = ids
	}
	if len(rejected) > 0 {
		m.editErr = "ignored: " + strings.Join(rejected, ", ")
	}
	return m.persist()
}

// filterBufMax bounds the launch-filter editor's buffer. One filter line is a handful of
// short clauses, so the ceiling is far above anything a person types and low enough that a
// stuck key cannot grow the buffer without limit. It is the exclude list's rule at the
// scale this row works at.
const filterBufMax = 512

// commitFilter parses the edited buffer into R9's launch filter and adopts it. The buffer is
// one line of the Feed's own filter grammar, parsed by filter.ParseQuery, the same door the
// Feed's / input uses, so the view can never store a filter the Feed could not state and a
// person learns one syntax rather than two.
//
// A line that does not parse leaves the setting exactly as it was and names the reason in
// the frame, which is the loader's R14 rule applied to input arriving by keystroke. It does
// not adopt a partial filter: ParseQuery rejects the whole line, and half a filter is a
// narrowing nobody asked for. An empty line commits the empty filter, which is how the
// gesture removes a launch filter as well as sets one.
func (m Model) commitFilter() Model {
	f, err := filter.ParseQuery(m.editBuf)
	if err != nil {
		m.editErr = "not applied: " + err.Error()
		return m
	}
	m.cfg.LaunchFilter = f
	return m.persist()
}

// applyCycle advances the selector under the cursor to the next value in its set, wrapping
// at the end (settings R5, R8, R19). The advance itself is the row's own, from the rows
// table, so a row with no cycle function is a row space does nothing on and this needs no
// arm per selector. The keybinding row's cycle is the one that reaches past its config
// field, and the table says why there.
func (m Model) applyCycle() Model {
	cycle := m.cursor.spec().cycle
	if cycle == nil {
		return m
	}
	return cycle(m)
}

// commitNumber adopts the typed buffer for the numeric row under the cursor, clamped to the
// setting's bound so the running view holds a value the file would accept (R12, R20, R21).
// An empty buffer is no change: enter on an untouched editor leaves the setting alone.
func (m Model) commitNumber() Model {
	if m.editBuf == "" {
		return m
	}
	v, err := strconv.Atoi(m.editBuf)
	if err != nil {
		return m
	}
	commit := m.cursor.spec().commit
	if commit == nil {
		return m
	}
	return commit(m, v).persist()
}

// persist writes the changed keys back through config.Save (R17, AC11). The baseline
// advances only on success, so a failed write is retried by the next edit rather than
// silently dropped, and the failure is held for the view to state.
func (m Model) persist() Model {
	if m.save == nil {
		return m
	}
	if err := m.save(m.initial, m.cfg); err != nil {
		m.saveErr = err
		return m
	}
	m.saveErr = nil
	m.initial = m.cfg
	return m
}

// clampRow keeps the cursor within the row range, so the motion keys stop at the ends
// rather than wrap; g and G reach the ends outright.
func clampRow(r row) row {
	if r < 0 {
		return 0
	}
	if r >= rowCount {
		return rowCount - 1
	}
	return r
}

// next advances v to the next member of set, wrapping, and returns the first member when v
// is not in it. A value the loader did not recognise cannot be cycled from where it is not,
// so the first member is where a cycle starts, which is also what the five per-type copies
// of this body did.
//
// The set is the caller's rather than derived from the type, so every call site passes the
// exported accessor config validates against, and the view therefore offers exactly what the
// loader accepts (R5, R6, R8, R10, R19). Deriving it here would need a registry keyed by
// type, which is a second place for the two to disagree.
func next[T comparable](v T, set []T) T {
	for i, m := range set {
		if v == m {
			return set[(i+1)%len(set)]
		}
	}
	return set[0]
}

// isDigit reports whether k is a plain digit press, the only text a numeric edit accepts,
// the same predicate the confirm pane uses for its typed count (R7).
func isDigit(k tea.KeyPressMsg) bool {
	s := k.String()
	return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
}

// listText is the text a key press contributes to an exclude-list edit, empty when the
// press contributes none. It reads KeyPressMsg.Text, the characters the press actually
// produced, rather than String(), which names a key rather than spelling it: String()
// answers "space" for the space bar, so a predicate over it would silently refuse the
// separator an OWNER/REPO list is written with.
//
// The accepted set is GitHub's owner and name charset plus the slash, dot, comma and
// space that qualify and separate entries, which is exactly what domain.ParseRepoRef and
// domain.NewRepoID between them admit. It is deliberately narrower than "any printable
// rune", so a pasted newline or a stray control sequence cannot enter the buffer.
func listText(k tea.KeyPressMsg) string {
	if len(k.Text) != 1 {
		return ""
	}
	c := k.Text[0]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return k.Text
	case c == '-', c == '_', c == '.', c == '/', c == ',', c == ' ':
		return k.Text
	default:
		return ""
	}
}

// filterText is the text a key press contributes to a launch-filter edit, empty when the
// press contributes none. Like listText it reads KeyPressMsg.Text rather than String(),
// which names a key rather than spelling it and answers "space" for the space bar, the one
// character the grammar separates clauses with.
//
// It admits any printable ASCII, which is wider than the exclude row's set and narrower
// than "any rune", both deliberately. Wider, because a filter's values are free-form: a
// branch, an actor, an event or a Workflow name is GitHub's to shape, and the grammar's own
// punctuation runs to colons, comparison operators and the range dots that gh's --created
// syntax uses. Narrower, because a pasted newline or a stray control sequence still has no
// business in a one-line filter.
func filterText(k tea.KeyPressMsg) string {
	if len(k.Text) != 1 {
		return ""
	}
	if c := k.Text[0]; c < ' ' || c > '~' {
		return ""
	}
	return k.Text
}
