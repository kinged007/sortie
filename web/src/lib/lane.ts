import { rowMark, timeBaseOf, timeText } from "@/lib/active-rows";
import { fmtStamp, parseInstant } from "@/lib/format";
import { reactionLabel, reactionShortLabel } from "@/lib/labels";
import { outcomeMark, runStatus } from "@/lib/run-status";

import type { Deciding } from "@/lib/active-rows";
import type {
  ActiveIssueResponse,
  AttentionItemResponse,
  ConfigResponse,
  IssueDetailResponse,
  IssueOutcomeResponse,
  PendingReactionResponse,
  RunResponse,
  ShapeReactions,
} from "@/api/types.gen";
import type { Mark } from "@/lib/run-status";

/** What a station stands for on the way from the tracker to the end of a pull request. */
export type StationKind = "tracker" | "agent" | "handoff" | "reaction" | "end";

/** One place an issue can stand; stations depend on the configuration alone, never on an issue. */
export interface Station {
  key: string;
  kind: StationKind;
  label: string;
  /** The form a header too narrow for the label uses; absent when the label is already short. */
  short?: string;
}

const AGENT_KEY = "agent";

const REACTION_ORDER = [
  "ci_failure",
  "review_comments",
  "bot_review",
  "merge_conflicts",
  "auto_merge",
  "merge_completion",
  "label_commands",
] as const satisfies readonly (keyof ShapeReactions)[];

/**
 * Lists the stations of a workflow in order: the tracker, one agent station
 * for all agent work, the handoff state, one station per configured reaction,
 * and the end once any reaction exists. Stage names never become stations.
 */
export function stationsOf(config: ConfigResponse): Station[] {
  const stations: Station[] = [
    { key: "tracker", kind: "tracker", label: "Tracker" },
    { key: AGENT_KEY, kind: "agent", label: "Agent" },
  ];
  const handoff = config.tracker.handoff_state;
  if (handoff !== undefined && handoff !== "") {
    stations.push({ key: "handoff", kind: "handoff", label: handoff });
  }
  const reactions = REACTION_ORDER.filter((key) => config.reactions[key] !== undefined);
  for (const key of reactions) {
    const short = reactionShortLabel(key);
    stations.push({
      key,
      kind: "reaction",
      label: reactionLabel(key),
      ...(short === undefined ? {} : { short }),
    });
  }
  if (reactions.length > 0) {
    stations.push({ key: "end", kind: "end", label: endLabelOf(config) });
  }
  return stations;
}

// Sortie records a terminal ending without a merge when an issue closes under an open pull
// request, so a terminal state's name is true of the end station only when every merge Sortie
// records moves the issue into it, which `merge_completion` does and `auto_merge` alone does not.
function endLabelOf(config: ConfigResponse): string {
  const terminal = config.tracker.terminal_states ?? [];
  const isEveryMergeTerminal =
    config.reactions.auto_merge === undefined || config.reactions.merge_completion !== undefined;
  return terminal.length === 1 && isEveryMergeTerminal ? (terminal[0] ?? "End") : "End";
}

function pendingEntryOf(
  rows: readonly PendingReactionResponse[],
  active: ActiveIssueResponse | undefined,
): PendingReactionResponse | undefined {
  return (
    rows.find((row) => row.kind === active?.reaction_kind) ??
    rows.find((row) => row.escalated_at !== null) ??
    rows.at(0)
  );
}

/**
 * Picks the state row that decides where an issue stands, from the issue's
 * status and its entry in the state's `active` list. A finished issue, an
 * unknown status, and a status whose row is absent decide nothing.
 */
export function decidingOf(
  detail: IssueDetailResponse,
  active: ActiveIssueResponse | undefined,
): Deciding | null {
  switch (detail.status) {
    case "running":
      return detail.running === null ? null : { kind: "running", entry: detail.running };
    case "retrying":
      return detail.retry === null ? null : { kind: "retrying", entry: detail.retry };
    case "budget_exhausted":
      return detail.budget_exhausted === null
        ? null
        : { kind: "budget_exhausted", entry: detail.budget_exhausted };
    case "parked":
      return detail.parked === null ? null : { kind: "parked", entry: detail.parked };
    case "between_stages":
      return detail.between_stages === null
        ? null
        : { kind: "between_stages", entry: detail.between_stages };
    case "pending_reactions": {
      const entry = pendingEntryOf(detail.pending_reactions ?? [], active);
      return entry === undefined ? null : { kind: "pending_reactions", entry };
    }
    default:
      return null;
  }
}

/** What {@link standingOf} reads: the deciding row, or the recorded ending when there is none. */
export interface StandingInput {
  deciding: Deciding | null;
  outcome: IssueOutcomeResponse | null;
  issueId: string;
  attention: readonly AttentionItemResponse[];
  /** The server clock as epoch milliseconds. */
  now: number;
}

/** What a lane draws at the station it stands on: a status mark, or the ring of an issue that ended today. */
export type LaneMarker = Mark | "ended";

/** Where an issue stands: a station key, its mark, and the time shown beside it. */
export interface Standing {
  station: string;
  mark: LaneMarker | null;
  time: string | null;
}

function reactionKeyOf(deciding: Deciding): string | undefined {
  switch (deciding.kind) {
    case "running":
    case "retrying":
      return deciding.entry.reaction_config_key;
    case "pending_reactions":
      return deciding.entry.config_key;
    default:
      return undefined;
  }
}

function decidingStation(deciding: Deciding, stations: readonly Station[]): string {
  const reactionKey = reactionKeyOf(deciding);
  const keyed = stations.find((s) => s.kind === "reaction" && s.key === reactionKey);
  if (keyed !== undefined) {
    return keyed.key;
  }
  if (deciding.kind === "pending_reactions") {
    return stations.find((s) => s.kind === "handoff")?.key ?? AGENT_KEY;
  }
  return AGENT_KEY;
}

function outcomeStation(kind: string, stations: readonly Station[]): Station | undefined {
  const wanted: StationKind | undefined =
    kind === "handed_off"
      ? "handoff"
      : kind === "merged" || kind === "terminal"
        ? "end"
        : undefined;
  return wanted === undefined
    ? undefined
    : (stations.find((s) => s.kind === wanted) ?? stations.at(-1));
}

/**
 * Places an issue on the stations. The deciding row wins; an issue with none
 * stands where its recorded ending puts it. It returns `null` when neither
 * exists, so a finished issue without a recorded ending has no marker.
 */
export function standingOf(input: StandingInput, stations: readonly Station[]): Standing | null {
  const { deciding, outcome, issueId, attention, now } = input;
  if (deciding !== null) {
    return {
      station: decidingStation(deciding, stations),
      mark: rowMark(deciding.kind, issueId, attention).mark,
      time: timeText(deciding.kind, timeBaseOf(deciding), now),
    };
  }
  if (outcome === null) {
    return null;
  }
  const station = outcomeStation(outcome.kind, stations);
  return station === undefined
    ? null
    : {
        station: station.key,
        mark: outcomeMark(outcome.kind),
        time: fmtStamp(outcome.observed_at),
      };
}

/** How a station reads against the issue's standing. */
export type StationState = "passed" | "current" | "ahead" | "waiting" | "escalated" | "idle";

/** A {@link Station} with its state, and the mark and time of the issue standing on it. */
export interface LaneStation extends Station {
  state: StationState;
  mark: LaneMarker | null;
  time: string | null;
}

function stateOf(
  station: Station,
  index: number,
  current: number,
  pending: readonly Pick<PendingReactionResponse, "config_key" | "escalated_at">[],
): StationState {
  if (station.kind === "reaction") {
    const rows = pending.filter((r) => r.config_key === station.key);
    if (rows.length === 0) {
      return "idle";
    }
    return rows.some((r) => r.escalated_at !== null) ? "escalated" : "waiting";
  }
  if (current < 0) {
    return "idle";
  }
  return index < current ? "passed" : "ahead";
}

/**
 * Gives each station its state against the standing: the current station
 * carries the standing's mark and time, a reaction station reads waiting or
 * escalated while a pull request row is pending on it, and the rest read
 * passed or ahead.
 */
export function laneOf(
  stations: readonly Station[],
  standing: Standing | null,
  pending: readonly Pick<PendingReactionResponse, "config_key" | "escalated_at">[],
): LaneStation[] {
  const current = standing === null ? -1 : stations.findIndex((s) => s.key === standing.station);
  return stations.map((station, index) =>
    standing !== null && index === current
      ? {
          ...station,
          state: "current",
          mark: standing.mark,
          time: standing.time,
        }
      : {
          ...station,
          state: stateOf(station, index, current, pending),
          mark: null,
          time: null,
        },
  );
}

/** The stage chain an issue is on and the stage it stands at. */
export interface StagePosition {
  chain: readonly string[];
  current: string;
}

function chainSourceOf(
  detail: IssueDetailResponse,
  deciding: Deciding | null,
): { chain: readonly string[] | null; current: string } | null {
  if (deciding !== null) {
    return deciding.entry.stage_position;
  }
  const last = detail.history?.last_stage;
  if (detail.status !== "history" || last === null || last === undefined) {
    return null;
  }
  return { chain: last.chain, current: last.rule };
}

/**
 * Reads a place in a stage chain from a chain source. It returns `null` when
 * there is no source, no chain, or the current stage is not on the chain.
 */
export function positionFrom(
  source: { chain: readonly string[] | null; current: string } | null,
): StagePosition | null {
  if (source === null || source.chain === null || !source.chain.includes(source.current)) {
    return null;
  }
  return { chain: source.chain, current: source.current };
}

/**
 * Reads the issue's place in its stage chain: from the deciding row while one
 * exists, from `history.last_stage` for a finished issue. It returns `null`
 * when the issue has no chain or its current stage is not on it.
 */
export function positionOf(
  detail: IssueDetailResponse,
  deciding: Deciding | null,
): StagePosition | null {
  return positionFrom(chainSourceOf(detail, deciding));
}

/**
 * Picks the newest finished run of a stage that was not started by a pull
 * request reaction, from runs in any order.
 */
export function lastStageRunOf(
  runs: readonly RunResponse[],
  current: string,
): RunResponse | undefined {
  return runs
    .filter((run) => run.rule_name === current && !run.reaction_kind)
    .reduce<RunResponse | undefined>(
      (newest, run) =>
        newest === undefined ||
        (parseInstant(run.started_at) ?? 0) > (parseInstant(newest.started_at) ?? 0) ||
        ((parseInstant(run.started_at) ?? 0) === (parseInstant(newest.started_at) ?? 0) &&
          run.run_id > newest.run_id)
          ? run
          : newest,
      undefined,
    );
}

/**
 * Reads a finished issue's place in the chain its last stage run stood on,
 * from one issue's runs served newest first. It returns `null` when the issue
 * has no stage run, or that run's chain is absent or does not hold its rule.
 */
export function lastStagePositionOf(runs: readonly RunResponse[]): StagePosition | null {
  const run = runs.find((candidate) => !candidate.reaction_kind && candidate.rule_name);
  return run === undefined
    ? null
    : positionFrom({ chain: run.stage_chain, current: run.rule_name ?? "" });
}

/** What the stage summary and the meter single out. */
export type StageFocus =
  | { kind: "current"; mark: Mark | null }
  | { kind: "hop_not_made"; target: string; result: string };

/** Counts for the stage summary and the stage meter. */
export interface StageProgress {
  /** The 1-based place of the current stage. */
  k: number;
  n: number;
  done: number;
  focus: StageFocus | null;
  remaining: number;
  remainingWord: "ahead" | "not run";
}

function hopNotMade(run: RunResponse | undefined): StageFocus | null {
  if (run === undefined || (run.stage_result !== "failed" && run.stage_result !== "ceiling")) {
    return null;
  }
  return {
    kind: "hop_not_made",
    target: run.stage_target ?? "",
    result: run.stage_result,
  };
}

/**
 * Counts the stages done and ahead and picks the focus of the summary.
 * `lastStageRun` is the newest finished non-reaction run of the current stage;
 * a `failed` or `ceiling` result on it means the hop to the next stage was
 * not made.
 */
export function stageProgressOf(
  position: StagePosition,
  live: boolean,
  station: StationKind | null,
  lastStageRun: RunResponse | undefined,
): StageProgress {
  const k = position.chain.indexOf(position.current) + 1;
  const n = position.chain.length;
  const remaining = n - k;
  const hop = hopNotMade(lastStageRun);

  if (live && station === "agent") {
    return {
      k,
      n,
      done: k - 1,
      focus: { kind: "current", mark: "current" },
      remaining,
      remainingWord: "ahead",
    };
  }
  if (live) {
    return {
      k,
      n,
      done: k,
      focus: hop,
      remaining,
      remainingWord: hop === null ? "ahead" : "not run",
    };
  }
  if (hop !== null || lastStageRun?.status === "succeeded") {
    return { k, n, done: k, focus: hop, remaining, remainingWord: "not run" };
  }
  const mark = lastStageRun === undefined ? null : runStatus(lastStageRun.status).mark;
  return {
    k,
    n,
    done: k - 1,
    focus: { kind: "current", mark },
    remaining,
    remainingWord: "not run",
  };
}
