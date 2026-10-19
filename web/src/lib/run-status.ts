/** The status marks a row, an outcome, or a run line can carry. */
export type Mark =
  | "running"
  | "current"
  | "waiting"
  | "retry"
  | "held"
  | "needs"
  | "stopped"
  | "handed"
  | "succeeded"
  | "failed"
  | "degraded"
  | "clear"
  | "stalled";

/** The marks that state where work stands; `stalled` is a separate marker beside them, never a state. */
export type StateMark = Exclude<Mark, "stalled">;

const RUN_STATUSES: Record<string, { word: string; mark: Mark | null }> = {
  succeeded: { word: "Succeeded", mark: "succeeded" },
  failed: { word: "Failed", mark: "failed" },
  ci_failed: { word: "CI failed", mark: "failed" },
  needs_person: { word: "Stopped for a person", mark: "stopped" },
  budget_stopped: { word: "Stopped at a budget", mark: "stopped" },
  cancelled: { word: "Cancelled", mark: null },
};

/** The persisted run statuses the dashboard words, in the order a menu lists them. */
export const RUN_STATUS_NAMES: readonly string[] = Object.keys(RUN_STATUSES);

/**
 * Gives the word and mark of a run status wherever the dashboard shows one: a
 * run line in the peek, a History run row, the History status menu, and the
 * Analytics "By run status" table. A run row serves its status and no wording
 * for it, so the client words the persisted statuses and shows any other value
 * as served with no mark.
 */
export function runStatus(status: string): { word: string; mark: Mark | null } {
  return Object.hasOwn(RUN_STATUSES, status) ? RUN_STATUSES[status] : { word: status, mark: null };
}

/**
 * Gives the mark of an issue outcome the dashboard marks: a handoff or a
 * merge. Any other kind, and an absent one, carries no mark.
 */
export function outcomeMark(kind: string | undefined): StateMark | null {
  switch (kind) {
    case "handed_off":
      return "handed";
    case "merged":
      return "succeeded";
    default:
      return null;
  }
}
