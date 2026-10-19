import { fmtCount, fmtMoney, fmtTokens, plural } from "@/lib/format";

import type { Summary } from "@/api/types.gen";

/** Words the tally line of Ended today from the stats summary of today's runs. */
export function tallyLine(
  summary: Summary,
  endedCount: number,
  tokenRatesConfigured: boolean,
): string {
  if (summary.runs === 0) {
    return "No run finished today.";
  }
  const parts = [
    `${plural(endedCount, "issue", "issues")} ended`,
    `${plural(summary.runs, "run", "runs")} finished`,
    `${fmtCount(summary.succeeded)} succeeded`,
    `${fmtCount(summary.failed)} failed`,
  ];
  if (summary.stopped > 0) {
    parts.push(`${fmtCount(summary.stopped)} stopped`);
  }
  if (summary.cancelled > 0) {
    parts.push(`${fmtCount(summary.cancelled)} cancelled`);
  }
  parts.push(tokensPart(summary));
  if (tokenRatesConfigured) {
    parts.push(costPart(summary));
  }
  return parts.join(" · ");
}

function tokensPart(summary: Summary): string {
  if (summary.tokens_unmeasured_runs === summary.runs) {
    return "tokens not measured";
  }
  if (summary.tokens === null) {
    return "tokens not recorded";
  }
  const { uncached } = summary.tokens;
  const text = `${fmtTokens(uncached)} ${uncached === 1 ? "token" : "tokens"}`;
  return summary.tokens_unmeasured_runs > 0
    ? `${text} (${plural(summary.tokens_unmeasured_runs, "run", "runs")} not measured)`
    : text;
}

function costPart(summary: Summary): string {
  if (summary.cost_usd === null) {
    return "cost unpriced";
  }
  const text = `${fmtMoney(summary.cost_usd)} estimated`;
  return summary.cost_unpriced_runs > 0
    ? `${text} (${plural(summary.cost_unpriced_runs, "run", "runs")} unpriced)`
    : text;
}
