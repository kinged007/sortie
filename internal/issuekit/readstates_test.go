package issuekit

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/waittest"
)

func TestReadStates_runsReadsConcurrently(t *testing.T) {
	t.Parallel()

	keys := make([]string, 2*maxConcurrentReads)
	for i := range keys {
		keys[i] = strconv.Itoa(i)
	}
	var inFlight, peak atomic.Int32
	full := make(chan struct{})
	var fullOnce sync.Once
	reads, err := ReadStates(waittest.Context(t), keys, func(ctx context.Context, key string) (domain.IssueStateAndLabels, bool, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		if n == maxConcurrentReads {
			fullOnce.Do(func() { close(full) })
		}
		select {
		case <-full:
			return domain.IssueStateAndLabels{State: "open"}, true, nil
		case <-ctx.Done():
			return domain.IssueStateAndLabels{}, false, ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("ReadStates() error = %v, want reads to run %d at a time", err, maxConcurrentReads)
	}
	if len(reads) != len(keys) {
		t.Errorf("ReadStates() returned %d reads, want %d", len(reads), len(keys))
	}
	if got := peak.Load(); got > maxConcurrentReads {
		t.Errorf("peak reads in flight = %d, want at most %d", got, maxConcurrentReads)
	}
}

func TestReadStates_omitsAbsentAndReturnsError(t *testing.T) {
	t.Parallel()

	reads, err := ReadStates(t.Context(), []string{"1", "2"}, func(_ context.Context, key string) (domain.IssueStateAndLabels, bool, error) {
		return domain.IssueStateAndLabels{State: "open"}, key == "1", nil
	})
	if err != nil {
		t.Fatalf("ReadStates() error = %v", err)
	}
	if _, ok := reads["2"]; ok || len(reads) != 1 {
		t.Errorf("ReadStates() = %v, want only key 1", reads)
	}

	boom := errors.New("boom")
	_, err = ReadStates(t.Context(), []string{"1", "2", "3"}, func(_ context.Context, key string) (domain.IssueStateAndLabels, bool, error) {
		if key == "2" {
			return domain.IssueStateAndLabels{}, false, boom
		}
		return domain.IssueStateAndLabels{State: "open"}, true, nil
	})
	if !errors.Is(err, boom) {
		t.Errorf("ReadStates() error = %v, want %v", err, boom)
	}
}
