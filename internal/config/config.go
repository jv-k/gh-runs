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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	}
	var diags []Diagnostic

	// The file layer sits above the defaults. A missing file leaves the defaults
	// in place (R3); a present one is resolved key by key, so one bad value cannot
	// discard the rest and no problem fails the run (R14).
	if data, ok := readConfigFile(env); ok {
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

// readConfigFile reads config.yml from the resolved config directory. A missing
// file, or no resolvable directory, reports no file rather than an error: both
// are valid and yield the defaults (R3).
func readConfigFile(env Env) ([]byte, bool) {
	dir := configDir(env, runtime.GOOS)
	if dir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		return nil, false
	}
	return data, true
}
