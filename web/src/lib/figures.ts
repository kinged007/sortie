import { fmtMoney, fmtTokens, plural } from "@/lib/format";

import type {
  Group,
  HistoryTotalsResponse,
  RunningEntryResponse,
  RunResponse,
} from "@/api/types.gen";

/** The report fields a token or cost text reads; a `Summary` satisfies it too. */
export type StatsFigures = Pick<
  Group,
  "tokens" | "cost_usd" | "tokens_unmeasured_runs" | "cost_unpriced_runs"
>;

/** The text of a token or cost figure that no run measured. */
export const NOT_MEASURED = "not measured";
/** The text of a cost the configured rates leave unpriced. */
export const UNPRICED = "unpriced";
const NOT_RECORDED = "not recorded";
const BASE_TIER = "base";

function atLeast(text: string): string {
  return `at least ${text}`;
}

/** Words an estimated cost by its served status; a status the client does not know reads "unknown". */
export function costText(status: string, usd: number | null): string {
  switch (status) {
    case "estimated":
      return usd === null ? "unknown" : fmtMoney(usd);
    case "partial":
      return usd === null ? "unknown" : atLeast(fmtMoney(usd));
    case "unpriced":
      return UNPRICED;
    case "unmeasured":
      return NOT_MEASURED;
    default:
      return "unknown";
  }
}

/** Words the token figure of one run. */
export function runTokensText(run: RunResponse): string {
  if (!run.tokens_measured) {
    return NOT_MEASURED;
  }
  const total = fmtTokens(run.tokens.total_tokens);
  return run.unaccounted_turns > 0 ? atLeast(total) : total;
}

/** Words the token figure of the session in flight; a count still arriving reads "at least". */
export function runningTokensText(running: RunningEntryResponse): string {
  const total = running.tokens.total_tokens;
  if (!running.tokens_measured || total === null) {
    return NOT_MEASURED;
  }
  return running.tokens_pending ? atLeast(fmtTokens(total)) : fmtTokens(total);
}

/** Words the estimated cost of one run. */
export function runCostText(run: RunResponse): string {
  return costText(run.cost_status, run.estimated_cost_usd);
}

/** Words the token figure over every run of an issue. */
export function totalsTokensText(totals: HistoryTotalsResponse): string {
  if (totals.tokens.total_tokens === null) {
    return NOT_MEASURED;
  }
  const total = fmtTokens(totals.tokens.total_tokens);
  return totals.tokens_complete ? total : atLeast(total);
}

/** Words the estimated cost over every run of an issue. */
export function totalsCostText(totals: HistoryTotalsResponse): string {
  return costText(totals.cost_status, totals.estimated_cost_usd);
}

/** Words the token figure of a report group or summary; a `base` tier records none. */
export function statsTokensText(f: StatsFigures, tier: string): string {
  if (tier === BASE_TIER) {
    return NOT_RECORDED;
  }
  if (f.tokens === null) {
    return f.tokens_unmeasured_runs > 0 ? NOT_MEASURED : NOT_RECORDED;
  }
  const total = fmtTokens(f.tokens.total);
  return f.tokens_unmeasured_runs > 0 ? atLeast(total) : total;
}

/** Words the estimated cost of a report group or summary; a cost with runs left out reads "at least". */
export function statsCostText(f: StatsFigures, tier: string): string {
  if (tier === BASE_TIER) {
    return NOT_RECORDED;
  }
  const hasShortfall = f.cost_unpriced_runs > 0 || f.tokens_unmeasured_runs > 0;
  if (f.cost_usd !== null) {
    const cost = fmtMoney(f.cost_usd);
    return hasShortfall ? atLeast(cost) : cost;
  }
  if (f.cost_unpriced_runs > 0) {
    return UNPRICED;
  }
  // A group of recorded CI results holds no measurable run, so it has no cost to state.
  return f.tokens_unmeasured_runs > 0 ? NOT_MEASURED : "none";
}

/** Words the runs the token figure leaves out, or `null` while it leaves none out. */
export function statsTokensShortfall(f: StatsFigures): string | null {
  return f.tokens_unmeasured_runs > 0
    ? plural(f.tokens_unmeasured_runs, "run not measured", "runs not measured")
    : null;
}

/** Words the runs the token and cost figures leave out, or `null` while they leave none out. */
export function statsShortfall(f: StatsFigures): string | null {
  const parts = [
    statsTokensShortfall(f),
    f.cost_unpriced_runs > 0 ? plural(f.cost_unpriced_runs, "run unpriced", "runs unpriced") : null,
  ].filter((part) => part !== null);
  return parts.length === 0 ? null : parts.join(" · ");
}
