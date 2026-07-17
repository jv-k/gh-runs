// Package clock is the injected clock. It imports nothing internal and lives in
// its own package because domain forbids I/O and reading the wall clock is I/O
// wearing a very small hat (ADR-0011). store (local-store R17), governor
// (rate-governor R21), scheduler, discovery and ops all inject it, so that tests
// of expiry, revalidation, the AIMD ramp and backoff advance time explicitly
// rather than sleeping.
//
// It is an alias, not an abstraction (ADR-0014). Declaring an interface of our
// own on top of clockwork would cost an adapter in every test that reaches for
// clockwork.NewFakeClock. The alias gives the importers one name and the tests
// the fake, unadapted.
package clock

import "github.com/jonboulle/clockwork"

// Clock is the injected clock five packages take (ADR-0011).
type Clock = clockwork.Clock

// Real returns the wall clock main.go injects in production.
func Real() Clock { return clockwork.NewRealClock() }
