// Package usagesource holds the measurement sources this transport reads
// outside the protocol wire, for runtimes whose protocol carries no spend
// counter.
//
// Every source here is vendor-specific by construction; the transport names
// none of them and asks each the same three questions in order: does it claim
// this launch, does the handshake report the runtime's name, and what has it
// read for this session so far. A no to either of the first two leaves the
// session unmeasured.
package usagesource

import (
	"context"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/registry"
)

// Constructor returns a fresh Reader for one session.
type Constructor func() Reader

// Sources is the registry every source adds itself to, exactly once, from an
// init function in its own file. The key is the name of the runtime the
// source reads, a name its Recognize accepts.
var Sources = registry.NewRegistry[Constructor, struct{}]("usage source")

// Reader is one measurement source. The transport declares its own interface
// with this method set and holds values of it.
//
// A Reader is used from two goroutines and only in this order: Claim and Close
// on the session's own goroutine, and Recognize, Open and Drain on the message
// pump. Drain is the one blocking method and runs on a goroutine the pump
// starts, not the pump itself.
type Reader interface {
	// Claim reports whether this source can measure a session launched against
	// target and, when it can, arms itself and returns the environment
	// assignments ("NAME=value") the launch needs. It refuses every launch with
	// target.RemoteCommand set. With runtime empty (the first offer), a source
	// whose claim returns an assignment or creates a file or directory claims
	// only a local launch whose command line (target.Command and every element
	// of target.Args) names the runtime it reads; a claim that returns no
	// assignment and creates nothing may be made for any local launch. With
	// runtime non-empty (the relaunch offer, carrying the name the first
	// start's handshake reported), it claims a local launch exactly when
	// Recognize(runtime) is true. A refusal returns nil, false and creates
	// nothing.
	Claim(target agentcore.LaunchTarget, runtime string) ([]string, bool)

	// Recognize reports whether name, the runtime's own name from the
	// handshake, is the runtime this source maps. No build is refused by its
	// version.
	Recognize(name string) bool

	// Open records the session identifier whose records this source counts and
	// the position in its own output the session starts at, so a resumed
	// runtime's history contributes nothing.
	Open(sessionID string)

	// Drain returns the session's run-cumulative usage as this source measured
	// it, waiting up to its own deadline for the turn's records to become
	// readable. lowerBound is the turn's own input-plus-output figure from the
	// protocol's presence signal, letting the wait end early; a non-positive
	// lowerBound means no such signal was read. found is false when the source
	// measured nothing at all, which is not a measurement of zero.
	Drain(ctx context.Context, lowerBound int64) (usage agentcore.RecoveredUsage, source string, found bool)

	// Completeness reports how far the figure the most recent Drain returned was
	// proven to account for that turn; a figure can be a measurement and still
	// be short of the turn. It describes one Drain call and is reset by the
	// next, so it is read on the goroutine that called Drain, before another
	// begins.
	Completeness() Completeness

	// Close releases whatever Claim armed. It is safe to call on a source that
	// never claimed.
	Close()
}

// Completeness is how far a drained figure was proven to account for the turn
// it was drained for. It is not a verdict on whether a measurement exists.
type Completeness int

const (
	// CompletenessUnknown is a turn with no bound to prove a figure against,
	// which differs from proving it short.
	CompletenessUnknown Completeness = iota

	// CompletenessPartial is a figure that did not reach the turn's own bound.
	CompletenessPartial

	// CompletenessAccounted is a figure that reached the turn's own bound.
	CompletenessAccounted
)
