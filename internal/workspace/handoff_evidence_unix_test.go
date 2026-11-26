//go:build unix

package workspace

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/waittest"
)

// fakeGitOnPath installs a shell script named "git" at the front of
// PATH for the duration of t, so every internal git invocation this
// test drives runs the script instead of the real binary. The script
// delegates to realGit unconditionally when after is "", and otherwise
// only when the script's own second argument does not equal after (so
// a caller can single out one subcommand for a held or escaped
// descendant while every other call passes through untouched).
func fakeGitOnPath(t *testing.T, body string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // fixture script under t.TempDir()
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCaptureHandoffEvidenceBaseline_HeldDescendantHoldingOutput pins
// that, with a held descendant holding the output of every git
// invocation the inspection makes, CaptureHandoffEvidenceBaseline
// returns with a computed fingerprint, and the last descendant is gone.
func TestCaptureHandoffEvidenceBaseline_HeldDescendantHoldingOutput(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found on PATH")
	}

	repoDir := newHandoffEvidenceGitWorkspace(t)

	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	fakeGitOnPath(t, "\""+realGit+"\" \"$@\"\n"+
		"sleep 30 & echo $! > '"+pidFile+"'\n")

	baseline, err := CaptureHandoffEvidenceBaseline(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("CaptureHandoffEvidenceBaseline() error = %v", err)
	}
	if baseline.Commit == "" {
		t.Error("baseline.Commit is empty, want the repository's HEAD commit")
	}
	var zeroFingerprint [sha256.Size]byte
	if baseline.WorktreeFingerprint == zeroFingerprint {
		t.Error("baseline.WorktreeFingerprint is the zero value, want a computed fingerprint")
	}

	waittest.ProcessGone(t, waittest.PIDFile(t, pidFile))
}

// TestCompareHandoffEvidenceBaseline_EscapedDescendantYieldsIncompleteOutputError
// pins that an escaped descendant holding the standard output of one
// git invocation makes CompareHandoffEvidenceBaseline return an error
// containing "output did not complete", because runGit's OutputComplete
// check is the one outcome rule that consults it.
func TestCompareHandoffEvidenceBaseline_EscapedDescendantYieldsIncompleteOutputError(t *testing.T) {
	agenttest.RequireSetsid(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found on PATH")
	}

	dir := newHandoffEvidenceGitWorkspace(t)
	baseline, err := CaptureHandoffEvidenceBaseline(context.Background(), dir)
	if err != nil {
		t.Fatalf("CaptureHandoffEvidenceBaseline() error = %v", err)
	}

	// "rev-parse --is-inside-work-tree" is the first call
	// inspectHandoffEvidenceState makes; a failure there is folded into
	// ErrNotGitWorkspace and would not surface the message under test,
	// so the fixture targets the second call instead, whose error
	// reaches the caller through a %w wrap that preserves it.
	// The sleep after backgrounding gives the forked child time to reach
	// its own setsid() call before this script exits and the reap's
	// group kill fires: without it, the child can still be killed
	// through the old process group in the race window between fork and
	// its own exec of setsid.
	fakeGitOnPath(t, "if [ \"$1\" = \"rev-parse\" ] && [ \"$2\" = \"--show-toplevel\" ]; then\n"+
		"  \""+realGit+"\" \"$@\"\n"+
		"  setsid sh -c 'sleep 30' &\n"+
		"  sleep 0.3\n"+
		"  exit 0\n"+
		"fi\n"+
		"exec \""+realGit+"\" \"$@\"\n")

	_, err = CompareHandoffEvidenceBaseline(context.Background(), dir, baseline)
	if err == nil {
		t.Fatal("CompareHandoffEvidenceBaseline() error = nil, want an error containing \"output did not complete\"")
	}
	if !strings.Contains(err.Error(), "output did not complete") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "output did not complete")
	}
}
