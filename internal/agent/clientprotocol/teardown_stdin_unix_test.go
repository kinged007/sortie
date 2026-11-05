//go:build unix

package clientprotocol

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sortie-ai/sortie/internal/waittest"
)

// blockingCloser is an io.Closer whose Close blocks until the test
// releases it, standing in for a stdin handle whose OS-level close
// never returns.
type blockingCloser struct {
	release chan struct{}
}

func (c blockingCloser) Close() error {
	<-c.release
	return nil
}

// TestStopSession_StdinCloseNeverReturns drives stopSession against a
// real subprocess, with state.stdinCloser replaced by a handle whose
// Close blocks forever. stopSession must still return, proving close_stdin
// never waits on that close.
func TestStopSession_StdinCloseNeverReturns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	state := newGracefulTeardownSession(t, teardownIgnoresGracefulScript(readyPath), readyPath, nil)
	state.agentConfig.StopGraceMS = 200

	release := make(chan struct{})
	state.stdinCloser = blockingCloser{release: release}
	t.Cleanup(func() { close(release) })

	done := make(chan error, 1)
	go func() { done <- stopSession(context.Background(), fakeSession(state)) }()

	if err := waittest.Receive(t, done, "stopSession() return against a stdin Close that never returns"); err != nil {
		t.Fatalf("stopSession() error = %v", err)
	}
}
