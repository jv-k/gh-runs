// Package clock is the injected clock. It imports nothing internal and lives in
// its own package because domain forbids I/O and reading the wall clock is I/O
// wearing a very small hat (ADR-0011). store (local-store R17), governor
// (rate-governor R21), scheduler, discovery and ops all inject it, so that tests
// of expiry, revalidation, the AIMD ramp and backoff advance time explicitly
// rather than sleeping.
package clock

import "time"

// Clock is the minimal reading surface the tool depends on. It is deliberately
// just Now: clockwork's fake satisfies it directly, which is the seam every
// timing-dependent test uses to make virtual time instant and deterministic.
type Clock interface {
	Now() time.Time
}

// System returns a Clock backed by the real wall clock. It is what main.go wires
// in production.
func System() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
