package sshutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
)

// ReverseForward asks one launch to expose an orchestrator-host loopback
// port on the remote host's loopback. The remote host chooses the remote
// port; the launch learns it through [SSHLaunch.WatchStderr].
type ReverseForward struct {
	// HostPort is the orchestrator-host loopback port, 1..65535.
	HostPort int

	// Render returns the outcome-dependent part of the preamble. It must be
	// pure: [BuildSSHLaunch] calls it with [WidestOutcome] to reserve length.
	Render func(ForwardOutcome) LatePreamble

	// Resolved, when non-nil, is called once with the real outcome, before
	// Render sees it.
	Resolved func(ForwardOutcome)
}

// ForwardStatus classifies how a forward request ended.
type ForwardStatus string

const (
	// ForwardAllocated means the remote host allocated a port.
	ForwardAllocated ForwardStatus = "allocated"

	// ForwardRefused means the remote host refused the forward.
	ForwardRefused ForwardStatus = "refused"

	// ForwardUnconfirmed means ssh reported neither an allocation nor a
	// refusal.
	ForwardUnconfirmed ForwardStatus = "unconfirmed"
)

// ForwardOutcome is the result of one forward request.
type ForwardOutcome struct {
	Status ForwardStatus

	// RemotePort is set only when Status is [ForwardAllocated].
	RemotePort int
}

// WidestOutcome is the outcome with the longest rendering, so a preamble
// reserved from it fits every real outcome.
var WidestOutcome = ForwardOutcome{Status: ForwardAllocated, RemotePort: 65535}

// ErrPreambleOverflow marks a late preamble longer than its reserved length.
var ErrPreambleOverflow = errors.New("late preamble exceeds its reserved length")

// LatePreamble is the outcome-dependent part of a launch's preamble. Renderings
// through [fmt], [log/slog] and [encoding/json] show Env names and the Args
// count, never a value, because both can hold a credential.
type LatePreamble struct {
	// Env is exported after [SSHOptions.Env] and follows the same name rules.
	Env []EnvVar

	// Args are the agent command's trailing arguments.
	Args []string
}

// Format implements [fmt.Formatter] for every verb.
func (p LatePreamble) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "sshutil.LatePreamble{Env:%v, Args:%d}", p.Env, len(p.Args)) //nolint:errcheck // a fmt.Formatter has no way to report a write error
}

// LogValue implements [slog.LogValuer].
func (p LatePreamble) LogValue() slog.Value {
	return slog.GroupValue(slog.Any("env", p.Env), slog.Int("args", len(p.Args)))
}

// MarshalJSON implements [json.Marshaler].
func (p LatePreamble) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Env  []EnvVar `json:"env"`
		Args int      `json:"args"`
	}{Env: p.Env, Args: len(p.Args)})
}

// forwardState is shared by every copy of a launch.
type forwardState struct {
	spec     *ReverseForward
	static   []EnvVar
	reserved int

	once     sync.Once
	done     chan struct{}
	outcome  ForwardOutcome // written before done closes
	preamble []byte         // written before done closes
	err      error          // written before done closes
}

func newForwardState(spec *ReverseForward) *forwardState {
	return &forwardState{spec: spec, done: make(chan struct{})}
}

// resolve runs Resolved and Render on the calling goroutine before releasing
// any waiter; later calls change nothing.
func (f *forwardState) resolve(outcome ForwardOutcome) {
	f.once.Do(func() {
		defer close(f.done)
		f.outcome = outcome
		if f.spec.Resolved != nil {
			f.spec.Resolved(outcome)
		}
		f.preamble, f.err = f.padPreamble(f.spec.Render(outcome))
	})
}

// padPreamble pads with spaces to the reserved length so the remote shell
// reads exactly the bytes it counted. Names are rechecked because Render saw
// only the widest outcome at build time and a name reaches the shell unquoted.
func (f *forwardState) padPreamble(late LatePreamble) ([]byte, error) {
	if i, reason := badEnvName(late.Env); i >= 0 {
		return nil, fmt.Errorf("sshutil: %s late environment variable name at Env[%d]", reason, i)
	}
	text := forwardPreamble(f.static, late)
	if len(text) > f.reserved {
		return nil, ErrPreambleOverflow
	}
	return []byte(text + strings.Repeat(" ", f.reserved-len(text))), nil
}

// await returns the padded preamble once the outcome resolves, or
// [io.ErrClosedPipe] when cancel closes first.
func (f *forwardState) await(cancel <-chan struct{}) ([]byte, error) {
	select {
	case <-f.done:
		return f.preamble, f.err
	case <-cancel:
		return nil, io.ErrClosedPipe
	}
}

// WatchStderr returns r minus the readiness marker line and resolves the
// forward outcome from what ssh and the remote shell write to r. It returns r
// unchanged for a launch without a forward.
//
// The first matching line decides the outcome; end of r without a match
// resolves as [ForwardUnconfirmed]. Resolved and Render run on the goroutine
// reading the returned reader, so the caller must keep reading until the
// outcome resolves, and before the first standard-input write or
// [SSHLaunch.WaitOutcome].
func (l SSHLaunch) WatchStderr(r io.Reader) io.Reader {
	if l.forward == nil {
		return r
	}
	return &stderrWatcher{src: r, state: l.forward, scanning: true}
}

// WaitOutcome returns the forward outcome once it resolves, or ctx.Err() when
// ctx ends first. A launch without a forward returns the zero outcome at once.
func (l SSHLaunch) WaitOutcome(ctx context.Context) (ForwardOutcome, error) {
	if l.forward == nil {
		return ForwardOutcome{}, nil
	}
	select {
	case <-l.forward.done:
		return l.forward.outcome, nil
	default:
	}
	select {
	case <-l.forward.done:
		return l.forward.outcome, nil
	case <-ctx.Done():
		return ForwardOutcome{}, ctx.Err()
	}
}

const (
	allocatedLinePrefix = "Allocated port "
	refusedLine         = "Warning: remote port forwarding failed for listen port 0"
)

// stderrWatcher scans lines only until the readiness marker: the remote shell
// prints it after ssh has said everything about the forward, so the rest
// passes unbuffered.
type stderrWatcher struct {
	src      io.Reader
	state    *forwardState
	scanning bool
	pending  []byte
	out      []byte
	err      error // returned once out drains
	chunk    [4096]byte
}

func (w *stderrWatcher) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(w.out) == 0 {
		if w.err != nil {
			return 0, w.err
		}
		n, err := w.src.Read(w.chunk[:])
		w.consume(w.chunk[:n])
		if err != nil {
			w.finish(err)
		}
	}
	n := copy(p, w.out)
	w.out = w.out[n:]
	return n, nil
}

func (w *stderrWatcher) consume(b []byte) {
	if !w.scanning {
		w.out = append(w.out, b...)
		return
	}
	w.pending = append(w.pending, b...)
	for w.scanning {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			return
		}
		w.scanLine(w.pending[:end+1])
		w.pending = w.pending[end+1:]
	}
	w.out = append(w.out, w.pending...)
	w.pending = nil
}

// finish scans an unterminated last line; an unmatched outcome is unconfirmed.
func (w *stderrWatcher) finish(err error) {
	if len(w.pending) > 0 {
		w.scanLine(w.pending)
		w.pending = nil
	}
	w.state.resolve(ForwardOutcome{Status: ForwardUnconfirmed})
	w.err = err
}

func (w *stderrWatcher) scanLine(line []byte) {
	text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	if text == readyMarker {
		w.state.resolve(ForwardOutcome{Status: ForwardUnconfirmed})
		w.scanning = false
		return
	}
	if outcome, ok := w.state.matchLine(text); ok {
		w.state.resolve(outcome)
	}
	w.out = append(w.out, line...)
}

func (f *forwardState) matchLine(text string) (ForwardOutcome, bool) {
	if text == refusedLine {
		return ForwardOutcome{Status: ForwardRefused}, true
	}
	middle, ok := strings.CutPrefix(text, allocatedLinePrefix)
	if !ok {
		return ForwardOutcome{}, false
	}
	port, ok := strings.CutSuffix(middle, " for remote forward to 127.0.0.1:"+strconv.Itoa(f.spec.HostPort))
	if !ok {
		return ForwardOutcome{}, false
	}
	remote, err := strconv.ParseUint(port, 10, 16)
	if err != nil || remote == 0 {
		return ForwardOutcome{}, false
	}
	return ForwardOutcome{Status: ForwardAllocated, RemotePort: int(remote)}, true
}
