module github.com/jv-k/gh-runs/v2

// The go directive is the contributor floor, computed from the dependencies
// rather than chosen: go-gh v2.13.0 and the Charm modules each declare go 1.25.0
// (ADR-0013). The toolchain directive is what actually builds, and moves only by
// a deliberate commit. setup-go reads toolchain in preference to go, so the two
// mean different things and drift apart on purpose.
go 1.25.0

toolchain go1.26.5

require (
	github.com/cli/go-gh/v2 v2.13.0
	github.com/jonboulle/clockwork v0.5.0
	gopkg.in/dnaeon/go-vcr.v4 v4.0.7
)

require (
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/cli/safeexec v1.0.0 // indirect
	github.com/cli/shurcooL-graphql v0.0.4 // indirect
	github.com/henvic/httpretty v0.0.6 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/thlib/go-timezone-local v0.0.0-20210907160436-ef149e42d28e // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/term v0.30.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
