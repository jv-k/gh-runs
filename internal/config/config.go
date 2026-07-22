// Package config loads the tool's settings from a single YAML file, applying
// precedence (flags, then file, then defaults) and emitting diagnostics rather
// than failing the run on a bad key (settings R3, R4, R14).
//
// R4's precedence names an environment layer between flags and file. No setting
// the tool allows reads an environment variable, so that layer is not built yet,
// and Load leaves a seam for it between the flag and file layers.
//
// It is not the Settings view. The file, its precedence and its defaults are
// needed by the governor before any view exists, so config sits at stage 0 and
// tui/settings arrives at stage 13 (ADR-0011, BUILD-ORDER). config imports domain
// alone and is imported by everything that reads a setting.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Env is an injected environment lookup, matching the shape of os.LookupEnv.
// main.go passes os.LookupEnv in production; a test passes a map-backed func
// pointed at a temporary $XDG_CONFIG_HOME (settings AC1, AC2).
type Env func(key string) (string, bool)

// Flags carries parsed CLI overrides, the highest-precedence source (R4). A
// zero-valued field means the flag was not passed, so it does not override. The
// CLI that populates this is stage 6.
type Flags struct {
	Budget Tier
}

// The confirm threshold's bounds (settings R12, AC8). The default and the hard
// maximum live here and nowhere else. There is no lower bound: a person may
// demand the typed confirmation for every deletion, so the floor is noLowerBound.
const (
	confirmThresholdDefault = 50
	confirmThresholdMax     = 500
)

// The Purge breaker's bounds (settings R21, AC13). Unlike the confirm threshold
// this clamps at both ends: no configuration may produce a Purge whose breaker
// never fires, so the floor is 1 rather than noLowerBound.
const (
	breakerFailuresDefault = 50
	breakerFailuresMin     = 1
	breakerFailuresMax     = 500
)

// The discovery revalidation interval's bounds (settings R20). It has a floor of
// 1 and no ceiling, so a large interval is honoured: the fast re-probe tier stays
// affordable at any value, and the hourly tier is a constant this key never
// touches.
const (
	discoveryRefreshDefault = 5
	discoveryRefreshMin     = 1
)

// noLowerBound and noUpperBound name the absent floor or ceiling of a setting
// bounded on one side only (R12, R20), rather than leaving a magic constant in
// the clamp call.
const (
	noLowerBound = math.MinInt
	noUpperBound = math.MaxInt
)

// Tier is the Budget setting: a named share of the primary rate limit the tool
// may spend polling, never a percentage and never an interval (settings R8 and
// its resolved open question). The governor maps a Tier to an internal rate;
// config only carries which tier was chosen.
type Tier string

const (
	TierBackground Tier = "background"
	TierNormal     Tier = "normal"
	TierGreedy     Tier = "greedy"
)

// tiers is the valid Budget set, in escalating order. It is the single source
// for both validation and the diagnostic that lists the valid values.
var tiers = []Tier{TierBackground, TierNormal, TierGreedy}

// valid reports whether t is one of the recognised tiers.
func (t Tier) valid() bool {
	for _, v := range tiers {
		if t == v {
			return true
		}
	}
	return false
}

// tierList renders the valid tiers for a diagnostic (R14).
func tierList() string {
	names := make([]string, len(tiers))
	for i, t := range tiers {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

// KeybindingProfile selects the motion set. There are exactly two, and a third
// is never added on the grounds of platform, because a terminal erases the
// distinction one would encode (settings R5, and its Constraints). The two
// differ on motion and nowhere else (live-run-feed R7).
type KeybindingProfile string

const (
	KeybindingStandard KeybindingProfile = "standard"
	KeybindingVim      KeybindingProfile = "vim"
)

// profiles is the valid keybinding set, the single source for validation and the
// diagnostic that lists the valid values (R5, AC4).
var profiles = []KeybindingProfile{KeybindingStandard, KeybindingVim}

// valid reports whether p is one of the two recognised profiles.
func (p KeybindingProfile) valid() bool {
	for _, v := range profiles {
		if p == v {
			return true
		}
	}
	return false
}

// profileList renders the valid profiles for a diagnostic (AC4).
func profileList() string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = string(p)
	}
	return strings.Join(names, ", ")
}

// Scope is the per-tab repository scope: the Workflows and Storage tabs each carry
// one independently, settable to all-repos or this-repo and defaulting to all-repos
// (settings R19). The Feed has no scope key: it expresses the same choice through the
// launch filter (R19's note), and this-repo's working-directory resolution and its
// fall-back-and-say-so are the consuming tab's, not this setting's.
type Scope string

const (
	ScopeAllRepos Scope = "all-repos"
	ScopeThisRepo Scope = "this-repo"
)

// scopes is the valid scope set, the single source for validation and the diagnostic
// that lists the valid values (R19).
var scopes = []Scope{ScopeAllRepos, ScopeThisRepo}

// valid reports whether s is one of the two recognised scopes.
func (s Scope) valid() bool {
	for _, v := range scopes {
		if s == v {
			return true
		}
	}
	return false
}

// scopeList renders the valid scopes for a diagnostic (R19).
func scopeList() string {
	names := make([]string, len(scopes))
	for i, s := range scopes {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// Tiers, KeybindingProfiles and Scopes return the valid values of each selector
// setting in the order the diagnostics list them, exported so the Settings view offers
// exactly the set Load validates against and cycles it in a documented order. Each
// returns a copy, so a caller cannot reorder or extend the registry the loader reads.
func Tiers() []Tier                           { return append([]Tier(nil), tiers...) }
func KeybindingProfiles() []KeybindingProfile { return append([]KeybindingProfile(nil), profiles...) }
func Scopes() []Scope                         { return append([]Scope(nil), scopes...) }

// Config is the resolved settings the rest of the tool reads.
type Config struct {
	Budget Tier
	// ConfirmThreshold is the set size at or above which a destructive action
	// requires the operator to type the affected count (settings R12). It has no
	// lower bound and a hard upper bound of confirmThresholdMax, so raising it
	// only lowers protection up to that ceiling (purge R7, R8).
	ConfirmThreshold int
	// BreakerFailures is the count of consecutive failures at which a Purge stops
	// itself (settings R21). Clamped to [breakerFailuresMin, breakerFailuresMax]:
	// there is no value at which the breaker never fires.
	BreakerFailures int
	// DiscoveryRefreshMinutes is the fast re-probe interval for discovery (settings
	// R20). Floored at discoveryRefreshMin with no ceiling, so a large interval is
	// honoured while a sub-minute one is refused.
	DiscoveryRefreshMinutes int
	// KeybindingProfile selects the motion set, Standard or Vim (settings R5). An
	// unrecognised profile is rejected and the default stands.
	KeybindingProfile KeybindingProfile
	// WorkflowsScope and StorageScope are the two tabs' independent repository scopes
	// (settings R19). Each is all-repos or this-repo, defaults to all-repos, and is
	// settable without disturbing the other. An unrecognised value is rejected and the
	// default stands.
	WorkflowsScope Scope
	StorageScope   Scope
}

// Diagnostic is a non-fatal message about the configuration: an unknown key, a
// rejected key with its reason, or a clamped value (R14). Load collects these and
// never fails a run over one.
type Diagnostic struct {
	Message string
}

// Load resolves the configuration from flags, the config file and the defaults,
// in that precedence (R4, minus the environment layer the package comment notes
// is unbuilt). A missing or empty config file is valid and yields the defaults
// (R3, AC1).
func Load(env Env, flags Flags) (Config, []Diagnostic) {
	// The defaults are the lowest-precedence layer (R3). Each number is the
	// spec's literal: the tier is normal, the confirm threshold is 50 (R12).
	cfg := Config{
		Budget:                  TierNormal,
		ConfirmThreshold:        confirmThresholdDefault,
		BreakerFailures:         breakerFailuresDefault,
		DiscoveryRefreshMinutes: discoveryRefreshDefault,
		KeybindingProfile:       KeybindingStandard,
		WorkflowsScope:          ScopeAllRepos,
		StorageScope:            ScopeAllRepos,
	}
	var diags []Diagnostic

	// The file layer sits above the defaults. A missing file leaves the defaults
	// in place (R3); an unreadable one leaves them too but says so (R14); a present
	// one is resolved key by key, so one bad value cannot discard the rest.
	data, present, fileDiags := readConfigFile(env)
	diags = append(diags, fileDiags...)
	if present {
		cfg, diags = resolveFile(cfg, data, diags)
	}

	// The flag layer is the highest, and applies whether or not a file was read
	// (R4). A zero-valued flag was not passed and does not override.
	if flags.Budget != "" {
		cfg.Budget = flags.Budget
	}

	return cfg, diags
}

// rejectedKeys maps each setting R13 refuses to the reason it is refused. R13's
// point is that these are absent for stated reasons rather than merely missing,
// so someone who reaches for one gets the reason and not a generic shrug (R14).
// Only poll_interval is named in AC7; the other four spellings are chosen here,
// and the reasons are R13's own.
var rejectedKeys = map[string]string{
	"poll_interval":      "the poll interval is mechanism, not intent. Choosing it needs the token tier, the repo count and the points model, which the scheduler has and you do not. The Budget tier is the setting for this",
	"deletes_per_second": "a fixed delete rate is mechanism, and dangerous. The adaptive governor beats any fixed number, whose only real function is getting an account blocked",
	"cache_ttl":          "a cache TTL is meaningless here. ETag revalidation is already free and correct, so a TTL could only make data staler",
	"concurrency":        "concurrency is internal. It is bounded by the secondary rate limit, not by taste",
	"skip_confirmation":  "a stored setting that skips confirmation is refused. It is the one thing standing between a keystroke and thousands of deleted Runs, and no stored form of it exists",
}

// resolveFile applies the file's keys to cfg and returns a diagnostic for every
// problem, never failing the run (R14). Each key is decoded from its own node, so
// a value of the wrong type falls that one setting back to its default and names
// itself, rather than discarding the whole file. A file that is not a settings
// mapping at all (a syntax error, or a list at the root) yields the defaults and
// one diagnostic. Keys are visited in sorted order so the diagnostics are stable.
func resolveFile(cfg Config, data []byte, diags []Diagnostic) (Config, []Diagnostic) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Not a usable mapping. The defaults stand and the run continues (R3), but
		// the person is told rather than left with a file silently ignored.
		return cfg, append(diags, Diagnostic{Message: fmt.Sprintf(
			"config.yml is not a settings mapping, using defaults (%s)", firstYAMLError(err))})
	}

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		node := raw[key]
		if node.Tag == "!!null" {
			continue // a present but empty value is treated as absent: keep the default
		}
		switch key {
		case "budget":
			var t Tier
			if node.Decode(&t) != nil {
				diags = append(diags, typeErr(key, "one of "+tierList(), node))
			} else if t.valid() {
				cfg.Budget = t
			} else {
				// An unrecognised tier is rejected, not adopted: the default stands
				// and the person gets an actionable message (R14, AC4).
				diags = append(diags, Diagnostic{Message: fmt.Sprintf(
					"budget: %q is not a valid tier; using %q. Valid tiers: %s",
					string(t), TierNormal, tierList())})
			}
		case "confirm_threshold":
			var v int
			if node.Decode(&v) != nil {
				diags = append(diags, typeErr(key, "a whole number", node))
			} else {
				cfg.ConfirmThreshold, diags = clampInt(key, v, noLowerBound, confirmThresholdMax, diags)
			}
		case "purge_breaker_failures":
			var v int
			if node.Decode(&v) != nil {
				diags = append(diags, typeErr(key, "a whole number", node))
			} else {
				cfg.BreakerFailures, diags = clampInt(key, v, breakerFailuresMin, breakerFailuresMax, diags)
			}
		case "discovery_refresh_minutes":
			var v int
			if node.Decode(&v) != nil {
				diags = append(diags, typeErr(key, "a whole number", node))
			} else {
				cfg.DiscoveryRefreshMinutes, diags = clampInt(key, v, discoveryRefreshMin, noUpperBound, diags)
			}
		case "keybinding_profile":
			var p KeybindingProfile
			if node.Decode(&p) != nil {
				diags = append(diags, typeErr(key, "one of "+profileList(), node))
			} else if p.valid() {
				cfg.KeybindingProfile = p
			} else {
				// Exactly two profiles exist, so a third is rejected (R5, AC4).
				diags = append(diags, Diagnostic{Message: fmt.Sprintf(
					"keybinding_profile: %q is not a valid profile; using %q. Valid profiles: %s",
					string(p), KeybindingStandard, profileList())})
			}
		case "workflows_scope":
			cfg.WorkflowsScope, diags = resolveScope(key, node, cfg.WorkflowsScope, diags)
		case "storage_scope":
			cfg.StorageScope, diags = resolveScope(key, node, cfg.StorageScope, diags)
		default:
			// Not a key this version applies. A key R13 refuses gets its specific
			// reason; anything else gets the generic unknown-key message (R14).
			if reason := rejectedKeys[key]; reason != "" {
				diags = append(diags, Diagnostic{Message: fmt.Sprintf("%s: %s", key, reason)})
			} else {
				diags = append(diags, Diagnostic{Message: fmt.Sprintf(
					"%s: unrecognised setting, ignored", key)})
			}
		}
	}
	return cfg, diags
}

// typeErr builds a diagnostic for a value of the wrong type. It names the key,
// what the key wanted, and the line, so the message is actionable in the way R14
// asks, rather than the parser's own "!!str into int" jargon.
func typeErr(key, want string, node yaml.Node) Diagnostic {
	return Diagnostic{Message: fmt.Sprintf(
		"%s: expected %s (line %d); using the default", key, want, node.Line)}
}

// firstYAMLError reduces a yaml error to its first line, dropping the parser's
// "yaml: unmarshal errors:\n" preamble so the diagnostic reads as one message.
func firstYAMLError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	return msg
}

// clampInt clamps v to [min, max]. When the value is out of range it returns the
// nearer bound and appends a diagnostic naming the key and the bound it hit, the
// clamp shape settings R12, R20 and R21 all share. A setting with no lower bound
// passes noLowerBound as min. An in-range value is returned unchanged with no
// diagnostic.
func clampInt(key string, v, min, max int, diags []Diagnostic) (int, []Diagnostic) {
	switch {
	case v < min:
		return min, append(diags, Diagnostic{Message: fmt.Sprintf(
			"%s: %d is below the minimum of %d; using %d", key, v, min, min)})
	case v > max:
		return max, append(diags, Diagnostic{Message: fmt.Sprintf(
			"%s: %d exceeds the maximum of %d; using %d", key, v, max, max)})
	default:
		return v, diags
	}
}

// resolveScope decodes a scope key, keeping the current value when the node is the
// wrong type or names a scope that does not exist (R19). It mirrors the profile's
// resolution: a value that is neither all-repos nor this-repo is rejected with a
// diagnostic listing both, and the default (the passed-in current value) stands.
func resolveScope(key string, node yaml.Node, current Scope, diags []Diagnostic) (Scope, []Diagnostic) {
	var s Scope
	if node.Decode(&s) != nil {
		return current, append(diags, typeErr(key, "one of "+scopeList(), node))
	}
	if s.valid() {
		return s, diags
	}
	return current, append(diags, Diagnostic{Message: fmt.Sprintf(
		"%s: %q is not a valid scope; using %q. Valid scopes: %s",
		key, string(s), current, scopeList())})
}

// ClampConfirmThreshold, ClampBreakerFailures and ClampDiscoveryRefresh apply the same
// bounds Load applies (R12, R21, R20), exported so the Settings view enforces them as it
// edits a running instance rather than deferring the clamp to the next Load. The view is
// the authority for the running instance (R17), so a value it holds must already be inside
// the bound the file would clamp it to.
func ClampConfirmThreshold(v int) int { return clampValue(v, noLowerBound, confirmThresholdMax) }
func ClampBreakerFailures(v int) int  { return clampValue(v, breakerFailuresMin, breakerFailuresMax) }
func ClampDiscoveryRefresh(v int) int { return clampValue(v, discoveryRefreshMin, noUpperBound) }

// clampValue is the bound arithmetic clampInt performs, without the diagnostic, for the
// exported view-facing clamps above.
func clampValue(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// configDir resolves the directory holding config.yml, following gh's verified
// precedence (settings R1, Constraints): $XDG_CONFIG_HOME if set on any platform,
// else $AppData on Windows, else $HOME/.config. goos is the target OS, injected so
// the Windows limb is testable on any host. It returns "" when no directory
// resolves, which Load treats as "no file", the same as a missing one (R3).
func configDir(env Env, goos string) string {
	if dir, ok := env("XDG_CONFIG_HOME"); ok && dir != "" {
		return filepath.Join(dir, "gh-runs")
	}
	if goos == "windows" {
		// Windows uses $AppData, never $HOME/.config, mirroring gh's fallback.
		if dir, ok := env("AppData"); ok && dir != "" {
			return filepath.Join(dir, "gh-runs")
		}
		return ""
	}
	if home, ok := env("HOME"); ok && home != "" {
		return filepath.Join(home, ".config", "gh-runs")
	}
	return ""
}

// readConfigFile reads config.yml from the resolved config directory. It reports
// whether a file was present to resolve, plus any diagnostic. A missing file, or
// no resolvable directory, is valid and quiet: both yield the defaults (R3). A
// file that exists but cannot be read (a permission error, an IO error, a
// directory in its place) yields a diagnostic instead of silently defaulting,
// because that silence would hide a misconfiguration the person needs to see.
func readConfigFile(env Env) (data []byte, present bool, diags []Diagnostic) {
	dir := configDir(env, runtime.GOOS)
	if dir == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	switch {
	case err == nil:
		return data, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	default:
		return nil, false, []Diagnostic{{Message: fmt.Sprintf(
			"config.yml could not be read, using defaults: %v", err)}}
	}
}

// Save persists the settings the running Settings view changed back to config.yml,
// writing only the keys that differ between prev and next and leaving everything else in
// the file untouched: comments, key order and keys this version does not recognise all
// survive (settings R17, AC11). prev is what the view opened with, so a field the operator
// never touched is not written, and a file that carried no such key stays without one. A
// Save that changes nothing writes nothing, so opening and closing the view creates no file
// (AC2's spirit). The write is atomic: the new content lands in a temporary file in the same
// directory and is renamed over config.yml, so a failed write never leaves a half-written or
// truncated config in its place (R17: the view must not corrupt the file).
//
// No secret is ever written here. The Config it marshals carries only display and behaviour
// choices; tokens live in the environment and the keyring and never enter this file (R2,
// ADR-0002).
func Save(env Env, prev, next Config) error {
	changes := changedKeys(prev, next)
	if len(changes) == 0 {
		return nil
	}
	dir := configDir(env, runtime.GOOS)
	if dir == "" {
		return errors.New("no config directory resolves; set $XDG_CONFIG_HOME or $HOME to persist settings")
	}
	path := filepath.Join(dir, "config.yml")

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	updated, err := applyChanges(existing, changes)
	if err != nil {
		return err
	}
	return writeFileAtomic(dir, path, updated)
}

// change is one key to write and the value to write under it. The slice changedKeys
// returns is ordered, so the keys a Save appends to a fresh file land in a stable order.
type change struct {
	key   string
	value any
}

// changedKeys returns the config.yml keys whose value differs between prev and next, in a
// fixed order. Each Config field maps to exactly one key, the same spelling resolveFile
// reads, so a value written here is read back by the next Load (R17). A string is written
// for the enum-typed settings and a bare int for the numeric ones, matching how the file
// spells each. Nothing here maps a rejected setting (R13): those have no field to change.
func changedKeys(prev, next Config) []change {
	var changes []change
	add := func(differs bool, key string, value any) {
		if differs {
			changes = append(changes, change{key: key, value: value})
		}
	}
	add(prev.Budget != next.Budget, "budget", string(next.Budget))
	add(prev.ConfirmThreshold != next.ConfirmThreshold, "confirm_threshold", next.ConfirmThreshold)
	add(prev.BreakerFailures != next.BreakerFailures, "purge_breaker_failures", next.BreakerFailures)
	add(prev.DiscoveryRefreshMinutes != next.DiscoveryRefreshMinutes, "discovery_refresh_minutes", next.DiscoveryRefreshMinutes)
	add(prev.KeybindingProfile != next.KeybindingProfile, "keybinding_profile", string(next.KeybindingProfile))
	add(prev.WorkflowsScope != next.WorkflowsScope, "workflows_scope", string(next.WorkflowsScope))
	add(prev.StorageScope != next.StorageScope, "storage_scope", string(next.StorageScope))
	return changes
}

// applyChanges edits the config document in place, rewriting each changed key's value node
// and appending a node pair for a key the file does not yet carry. It round-trips through a
// yaml.Node so comments, key order and unrecognised keys survive the edit (R17): only the
// value nodes the change set names are touched, and each keeps the comments that sat on it.
func applyChanges(existing []byte, changes []change) ([]byte, error) {
	var doc yaml.Node
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := yaml.Unmarshal(existing, &doc); err != nil {
			return nil, fmt.Errorf("config.yml is not valid YAML, refusing to overwrite it: %w", err)
		}
	}
	mapping := documentMapping(&doc)
	if mapping == nil {
		return nil, errors.New("config.yml is not a settings mapping, refusing to overwrite it")
	}
	for _, c := range changes {
		setMappingKey(mapping, c.key, c.value)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// documentMapping returns the root mapping node to edit, building an empty document and
// mapping when the file was absent or blank. It returns nil when a present root is not a
// mapping (a list, a scalar), which Save refuses rather than clobber.
func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{mapping}
		return mapping
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	return nil
}

// setMappingKey sets key to value in the mapping, rewriting the existing value node in
// place so its comments and position are kept, or appending a new key/value pair when the
// key is absent. A mapping node's Content is the flat [k0, v0, k1, v1, ...] sequence yaml.v3
// uses, so the value of a key is the node right after it.
func setMappingKey(mapping *yaml.Node, key string, value any) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			setScalar(mapping.Content[i+1], value)
			return
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{}
	setScalar(valueNode, value)
	mapping.Content = append(mapping.Content, keyNode, valueNode)
}

// setScalar writes value into node as a plain scalar, preserving any comments the node
// carried so an inline note on the edited line survives the change (R17). Every settings
// value is a plain string or a whole number, so the node needs no quoting style.
func setScalar(node *yaml.Node, value any) {
	head, line, foot := node.HeadComment, node.LineComment, node.FootComment
	node.Kind = yaml.ScalarNode
	node.Style = 0
	node.Content = nil
	switch v := value.(type) {
	case string:
		node.Tag = "!!str"
		node.Value = v
	case int:
		node.Tag = "!!int"
		node.Value = strconv.Itoa(v)
	default:
		node.Tag = "!!str"
		node.Value = fmt.Sprint(v)
	}
	node.HeadComment, node.LineComment, node.FootComment = head, line, foot
}

// writeFileAtomic writes data to path by way of a temporary file in the same directory,
// renamed into place, so a reader never sees a partial config and a failed write never
// truncates the existing one (R17). The directory is created if absent (R1's default
// location), the file is left mode 0600, and the temporary is removed on any error before
// the rename.
func writeFileAtomic(dir, path string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "config-*.yml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
