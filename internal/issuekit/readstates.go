package issuekit

import (
	"context"
	"sync"

	"github.com/sortie-ai/sortie/internal/domain"
)

// maxConcurrentReads bounds the per-issue requests a forge without a batch
// read has in flight, so a read over many issues does not wait on them one
// by one and still stays far below a forge's concurrency limits.
const maxConcurrentReads = 8

// ReadStates calls read once per key, with at most 8 calls running at a
// time, and returns the reads keyed by key. A key whose read reports ok
// false is left out.
//
// The first error cancels the calls still running and is returned. When ctx
// ends first, ReadStates returns its error.
func ReadStates(ctx context.Context, keys []string, read func(ctx context.Context, key string) (state domain.IssueStateAndLabels, ok bool, err error)) (map[string]domain.IssueStateAndLabels, error) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	reads := make(map[string]domain.IssueStateAndLabels, len(keys))
	slots := make(chan struct{}, maxConcurrentReads)
	for _, key := range keys {
		select {
		case slots <- struct{}{}:
		case <-readCtx.Done():
		}
		if readCtx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			state, ok, err := read(readCtx, key)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				if firstErr == nil {
					firstErr = err
					cancel()
				}
			case ok:
				reads[key] = state
			}
		})
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return reads, nil
}
