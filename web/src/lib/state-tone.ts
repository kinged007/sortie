import type { ShapeTracker } from "@/api/types.gen";

/** Where a tracker state sits in the workflow: not started, being worked, or finished. */
export type StateTone = "idle" | "progress" | "done";

function same(a: string, b: string | undefined): boolean {
  return b !== undefined && a.toLowerCase() === b.toLowerCase();
}

/**
 * Places a tracker state by the configured state lists: a terminal state is
 * done; the in-progress state, the handoff state, and every active state after
 * the first are in progress; anything else, including an unknown tracker, is idle.
 */
export function stateToneOf(state: string, tracker: ShapeTracker | undefined): StateTone {
  if (tracker === undefined) {
    return "idle";
  }
  if ((tracker.terminal_states ?? []).some((terminal) => same(state, terminal))) {
    return "done";
  }
  if (same(state, tracker.in_progress_state) || same(state, tracker.handoff_state)) {
    return "progress";
  }
  const active = tracker.active_states ?? [];
  return active.slice(1).some((entry) => same(state, entry)) ? "progress" : "idle";
}
