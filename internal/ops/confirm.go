package ops

import (
	"errors"
	"strconv"
	"sync/atomic"
)

// ErrDeclined is returned when an Input does not satisfy a Plan's friction: the
// wrong key, an empty answer, or a mistyped count. A declined Confirm changes
// nothing, issues no request, and leaves the Plan valid to collect again (AC6, AC7).
var ErrDeclined = errors.New("ops: confirmation declined")

// ErrSpent is returned when Execute is called a second time on one Confirmed. A
// Confirmed proves one act of confirmation, so it authorises one execution
// (ADR-0019).
var ErrSpent = errors.New("ops: this confirmation has already been executed")

// inputKind is which of the three constructors built an Input. It is unexported,
// so an Input can only be one of NoInput, Answer or NonInteractiveYes, and a stored
// setting is expressible as none of them (settings R13, ADR-0019).
type inputKind int

const (
	inputNone inputKind = iota
	inputAnswer
	inputNonInteractiveYes
)

// Input is one explicit act of confirmation, built only through a constructor. The
// TUI collects an Answer; the CLI passes NonInteractiveYes for its --yes flag; a
// FrictionNone write passes NoInput. No constructor expresses a stored setting,
// which is exactly the line settings R13 draws (ADR-0019).
type Input struct {
	kind   inputKind
	answer string
}

// NoInput is the empty act a FrictionNone write carries: the pane opens no modal,
// and Confirm succeeds trivially (ADR-0019, run-lifecycle R18).
func NoInput() Input { return Input{kind: inputNone} }

// Answer is what a modal collected: y, or a typed count (purge R7).
func Answer(s string) Input { return Input{kind: inputAnswer, answer: s} }

// NonInteractiveYes is cli-surface R11's --yes, and nothing else: an explicit act
// made once per invocation on the surface with no modal to show. Its constructor's
// name is what keeps the TUI honest, because a tab passing it is a greppable lie
// AC9's zero-request assertions catch (ADR-0019).
func NonInteractiveYes() Input { return Input{kind: inputNonInteractiveYes} }

// Confirmed is proof of confirmation, with unexported fields so ops.Confirm is its
// only constructor. It proves three things (ADR-0019): its set came out of Plan
// (frozen, eligibility-stamped, friction-priced); the priced friction was satisfied
// by an explicit act; and at most one execution, through the spent cell below.
type Confirmed struct {
	plan  Plan
	spent *atomic.Bool
}

// Confirm validates in against p's friction and returns a Confirmed, or ErrDeclined.
// The validation table (ADR-0019): FrictionNone accepts any Input; FrictionYN
// accepts Answer("y") and NonInteractiveYes; FrictionTypedCount accepts an Answer
// carrying exactly Total()'s decimal digits, and NonInteractiveYes. Everything else
// is declined, and a declined Confirm issues nothing (AC6, AC7). The spent cell is a
// fresh pointer every copy of the returned Confirmed shares, so single use survives
// pass-by-value (ADR-0019).
func (o *Ops) Confirm(p Plan, in Input) (Confirmed, error) {
	if !satisfies(p.friction, p.Total(), in) {
		return Confirmed{}, ErrDeclined
	}
	return Confirmed{plan: p, spent: &atomic.Bool{}}, nil
}

// satisfies is Confirm's validation table. NonInteractiveYes satisfies every level,
// because cli-surface R11 defines the flag as that surface's confirmation, an
// explicit act, never a skip of one (ADR-0019).
func satisfies(level FrictionLevel, total int, in Input) bool {
	if in.kind == inputNonInteractiveYes {
		return true
	}
	switch level {
	case FrictionNone:
		return true
	case FrictionYN:
		return in.kind == inputAnswer && in.answer == "y"
	case FrictionTypedCount:
		return in.kind == inputAnswer && in.answer == strconv.Itoa(total)
	default:
		return false
	}
}
