// Package agentcore provides shared utilities used by agent adapter
// packages. It must not import any specific adapter package (claude,
// copilot, codex, etc.). Its allowed internal imports are
// internal/domain, internal/agent/procutil, internal/agent/sshutil,
// and internal/typeutil.
package agentcore

import "time"

// toolEntry holds the start-of-execution metadata and the mapped call for a
// single in-flight tool call. ts uses a monotonic clock reading so that time.Since
// arithmetic is accurate regardless of wall-clock adjustments.
type toolEntry struct {
	call ToolCall
	ts   time.Time
}

// ToolTracker correlates tool-start events with tool-completion events
// and computes execution duration. It is not safe for concurrent use;
// each RunTurn invocation constructs its own ToolTracker and uses it on
// a single goroutine.
type ToolTracker struct {
	entries map[string]toolEntry
}

// NewToolTracker returns an empty ToolTracker ready for use.
func NewToolTracker() *ToolTracker {
	return &ToolTracker{entries: make(map[string]toolEntry)}
}

// Begin records the start of the tool call identified by id. If id was
// already registered, the previous entry is overwritten.
func (t *ToolTracker) Begin(id string, call ToolCall) {
	t.entries[id] = toolEntry{call: call, ts: time.Now()}
}

// End records the completion of a tool execution identified by id.
// Returns the call Begin stored, elapsed duration in milliseconds, and
// ok=true on a hit. Returns (ToolCall{}, 0, false) if id was never
// registered. durationMS is clamped to 0 for non-positive values.
func (t *ToolTracker) End(id string) (call ToolCall, durationMS int64, ok bool) {
	entry, exists := t.entries[id]
	if !exists {
		return ToolCall{}, 0, false
	}
	delete(t.entries, id)
	ms := max(time.Since(entry.ts).Milliseconds(), 0)
	return entry.call, ms, true
}
