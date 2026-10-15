import {
  statsCostText,
  statsShortfall,
  statsTokensShortfall,
  statsTokensText,
} from "@/lib/figures";
import { fmtCount, fmtDuration, fmtMean, fmtPercent, plural } from "@/lib/format";

import type { ConfigResponse, Report } from "@/api/types.gen";
import type { ReactNode } from "react";

interface TileProps {
  label: string;
  value: string;
  detail?: string | null;
}

function Tile({ label, value, detail }: TileProps) {
  return (
    <div className="flex min-w-0 flex-col gap-xxs rounded-lg border border-line bg-canvas px-md py-sm">
      <span className="text-label-caps font-medium text-ink-muted">{label}</span>
      <span className="font-heading text-h6 font-semibold text-ink tabular-nums [overflow-wrap:anywhere]">
        {value}
      </span>
      {detail === undefined || detail === null ? undefined : (
        <span className="text-body-sm text-ink-muted [overflow-wrap:anywhere]">{detail}</span>
      )}
    </div>
  );
}

function runsDetail(summary: Report["summary"]): string | null {
  const parts = [
    summary.failed > 0 ? `${fmtCount(summary.failed)} failed` : null,
    summary.stopped > 0 ? `${fmtCount(summary.stopped)} stopped` : null,
    summary.cancelled > 0 ? `${fmtCount(summary.cancelled)} cancelled` : null,
  ].filter((part) => part !== null);
  return parts.length === 0 ? null : parts.join(" · ");
}

/** Props of {@link SummaryTiles}. */
export interface SummaryTilesProps {
  report: Report;
  config: ConfigResponse | undefined;
}

/** Renders the success-rate hero and the summary tiles of a report; every figure is one report field passed through a formatter. */
export function SummaryTiles({ report, config }: SummaryTilesProps) {
  const { summary, schema_tier: tier } = report;
  const hasSucceeded = summary.succeeded > 0;
  const tiles: ReactNode[] = [
    <Tile key="runs" label="Runs" value={fmtCount(summary.runs)} detail={runsDetail(summary)} />,
    <Tile key="issues" label="Issues" value={fmtCount(summary.issues)} />,
  ];
  if (hasSucceeded) {
    tiles.push(
      <Tile
        key="duration"
        label="Duration, succeeded runs"
        value={fmtDuration(summary.duration_seconds.p50)}
        detail={`p50 · p95 ${fmtDuration(summary.duration_seconds.p95)}`}
      />,
      <Tile
        key="turns"
        label="Turns per succeeded run"
        value={
          summary.mean_turns_succeeded === null
            ? "not recorded"
            : fmtMean(summary.mean_turns_succeeded)
        }
      />,
    );
  }
  tiles.push(
    <Tile
      key="tokens"
      label="Tokens"
      value={statsTokensText(summary, tier)}
      detail={statsTokensShortfall(summary) ?? (tier === "full" ? "every run measured" : null)}
    />,
  );
  if (config?.token_rates != null) {
    tiles.push(
      <Tile
        key="cost"
        label="Estimated cost"
        value={statsCostText(summary, tier)}
        detail={statsShortfall(summary)}
      />,
    );
  }

  return (
    <div className="flex flex-col gap-lg">
      <div className="flex flex-col gap-xxs">
        <span className="font-heading text-h3 font-semibold text-ink tabular-nums">
          {fmtPercent(summary.success_rate)}
        </span>
        <span className="text-body-md text-ink-body">
          {fmtCount(summary.succeeded)} of {plural(summary.runs, "run", "runs")} succeeded
        </span>
      </div>
      <div className="grid grid-cols-2 gap-sm md:grid-cols-[repeat(auto-fit,minmax(12rem,1fr))]">
        {tiles}
      </div>
    </div>
  );
}
