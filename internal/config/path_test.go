package config

import (
	"path/filepath"
	"testing"
)

// lookup builds an Env from a map, for the path cases below.
func lookup(m map[string]string) Env {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// TestConfigDir pins settings R1's full path precedence across platforms: XDG
// first everywhere, then $AppData on Windows and $HOME/.config elsewhere. It
// exercises configDir directly because the Windows limb cannot run through Load
// on a non-Windows host, and only the injected OS branch differs.
func TestConfigDir(t *testing.T) {
	cases := []struct {
		name string
		goos string
		vars map[string]string
		want string
	}{
		{
			name: "XDG wins on Unix",
			goos: "linux",
			vars: map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/home/u"},
			want: filepath.Join("/xdg", "gh-runs"),
		},
		{
			name: "Unix falls back to HOME/.config when XDG unset",
			goos: "linux",
			vars: map[string]string{"HOME": "/home/u"},
			want: filepath.Join("/home/u", ".config", "gh-runs"),
		},
		{
			name: "XDG wins on Windows too",
			goos: "windows",
			vars: map[string]string{"XDG_CONFIG_HOME": "/xdg", "AppData": `C:\Users\u\AppData\Roaming`},
			want: filepath.Join("/xdg", "gh-runs"),
		},
		{
			name: "Windows falls back to AppData when XDG unset",
			goos: "windows",
			vars: map[string]string{"AppData": `C:\Users\u\AppData\Roaming`},
			want: filepath.Join(`C:\Users\u\AppData\Roaming`, "gh-runs"),
		},
		{
			name: "Windows does not consult HOME",
			goos: "windows",
			vars: map[string]string{"HOME": "/home/u"}, // no XDG, no AppData
			want: "",
		},
		{
			name: "nothing resolvable returns empty",
			goos: "linux",
			vars: map[string]string{},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := configDir(lookup(c.vars), c.goos); got != c.want {
				t.Fatalf("configDir(goos=%q) = %q, want %q", c.goos, got, c.want)
			}
		})
	}
}
