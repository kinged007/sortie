package sshutil

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"

	"github.com/sortie-ai/sortie/internal/waittest"
)

func forwardedLaunch() SSHLaunch {
	return BuildSSHLaunch("h", "/ws", "agent", nil, SSHOptions{
		Forward: &ReverseForward{HostPort: 4242, Render: toolsURLRender},
	})
}

func TestWatchStderr_ResolvesOutcomeAndDropsOnlyTheMarker(t *testing.T) {
	t.Parallel()

	const (
		alloc   = "Allocated port 40000 for remote forward to 127.0.0.1:4242\n"
		refused = "Warning: remote port forwarding failed for listen port 0\n"
		marker  = "sortie: remote shell ready\n"
	)
	allocated := ForwardOutcome{Status: ForwardAllocated, RemotePort: 40000}
	refusedOutcome := ForwardOutcome{Status: ForwardRefused}
	unconfirmed := ForwardOutcome{Status: ForwardUnconfirmed}
	tests := []struct {
		name    string
		stderr  string
		want    string
		outcome ForwardOutcome
	}{
		{"allocation before the marker", "noise\n" + alloc + marker + "agent\n", "noise\n" + alloc + "agent\n", allocated},
		{"refusal before the marker", refused + marker + "agent\n", refused + "agent\n", refusedOutcome},
		{"marker alone", marker + "agent\n", "agent\n", unconfirmed},
		{"carriage returns", "noise\r\n" + strings.Replace(alloc, "\n", "\r\n", 1) + "sortie: remote shell ready\r\nagent\r\n", "noise\r\n" + strings.Replace(alloc, "\n", "\r\n", 1) + "agent\r\n", allocated},
		{"first match wins", refused + alloc + marker, refused + alloc, refusedOutcome},
		{"allocation for another host port", "Allocated port 40000 for remote forward to 127.0.0.1:4243\n" + marker, "Allocated port 40000 for remote forward to 127.0.0.1:4243\n", unconfirmed},
		{"allocated port out of range", "Allocated port 65536 for remote forward to 127.0.0.1:4242\n" + marker, "Allocated port 65536 for remote forward to 127.0.0.1:4242\n", unconfirmed},
		{"allocation after the marker", marker + alloc, alloc, unconfirmed},
		{"stream ends before the marker", "noise\nunterminated", "noise\nunterminated", unconfirmed},
	}
	readers := []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"whole", func(r io.Reader) io.Reader { return r }},
		{"one byte at a time", iotest.OneByteReader},
	}

	for _, tt := range tests {
		for _, rd := range readers {
			t.Run(tt.name+"/"+rd.name, func(t *testing.T) {
				t.Parallel()

				launch := forwardedLaunch()

				got, err := io.ReadAll(launch.WatchStderr(rd.wrap(strings.NewReader(tt.stderr))))
				if err != nil {
					t.Fatalf("reading WatchStderr: %v", err)
				}
				outcome, waitErr := launch.WaitOutcome(waittest.Context(t))

				if string(got) != tt.want {
					t.Errorf("WatchStderr(%q) = %q, want %q", tt.stderr, got, tt.want)
				}
				if waitErr != nil || outcome != tt.outcome {
					t.Errorf("WaitOutcome after %q = %v, %v, want %v, nil", tt.stderr, outcome, waitErr, tt.outcome)
				}
			})
		}
	}
}

func TestPrefixStdin_ForwardedWritesNothingBeforeTheOutcomeLine(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		launch := forwardedLaunch()
		stdin := &recordingWriteCloser{}
		stderrR, stderrW := io.Pipe()
		go io.Copy(io.Discard, launch.WatchStderr(stderrR)) //nolint:errcheck // the copy ends when the test closes the pipe and nothing waits on its result
		written := make(chan error, 1)
		go func() {
			_, err := launch.PrefixStdin(stdin).Write([]byte("agent bytes"))
			written <- err
		}()

		synctest.Wait()
		if got := stdin.recordedWrites(); len(got) != 0 {
			t.Fatalf("stdin writes before any stderr line = %q, want none", got)
		}
		io.WriteString(stderrW, "ssh: connecting\n") //nolint:errcheck // a pipe write fails only once the reader has closed, and the watcher reads until the test ends
		synctest.Wait()
		if got := stdin.recordedWrites(); len(got) != 0 {
			t.Fatalf("stdin writes before the outcome line = %q, want none", got)
		}
		io.WriteString(stderrW, "Allocated port 40000 for remote forward to 127.0.0.1:4242\n") //nolint:errcheck // a pipe write fails only once the reader has closed, and the watcher reads until the test ends
		if err := waittest.Receive(t, written, "Write after the outcome line"); err != nil {
			t.Fatalf("Write after the outcome line = %v, want nil", err)
		}

		got := string(bytes.Join(stdin.recordedWrites(), nil))
		if !strings.HasPrefix(got, "unset _sortie_env") || !strings.HasSuffix(got, "agent bytes") {
			t.Errorf("stdin after the outcome line = %q, want the preamble then %q", got, "agent bytes")
		}
		stderrW.Close() //nolint:errcheck // PipeWriter.Close always returns nil
	})
}
