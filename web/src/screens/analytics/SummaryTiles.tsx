import {
  statsCacheReadsText,
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
  detail?: ReactNode;
}

// Spans three rows of the shared figure grid so labels, figures and detail lines
// of one visual row stay level when a label wraps. The grid sets no row gap
// because a subgrid inherits it between its own rows; the label margin spaces
// the visual rows instead.
function Tile({ label, value, detail }: TileProps) {
  return (
    <div className="row-span-3 grid min-w-0 grid-rows-subgrid">
      <span className="mt-lg text-body-sm text-ink-muted">{label}</span>
      <span className="mt-xxs font-heading text-h5 font-semibold text-ink tabular-nums">
        {value}
      </span>
      {detail === undefined || detail === null ? undefined : (
        <span className="mt-xxs text-body-sm text-ink-muted">{detail}</span>
      )}
    </div>
  );
}

function runsDetail(summary: Report["summary"]): ReactNode {
  const parts = [
    summary.failed > 0 ? `${fmtCount(summary.failed)} failed` : undefined,
    summary.stopped > 0 ? `${fmtCount(summary.stopped)} stopped` : undefined,
    summary.cancelled > 0 ? `${fmtCount(summary.cancelled)} cancelled` : undefined,
  ].filter((part) => part !== undefined);
  return parts.length === 0 ? undefined : (
    <span className="flex flex-wrap gap-x-md">
      {parts.map((part) => (
        <span key={part} className="whitespace-nowrap">
          {part}
        </span>
      ))}
    </span>
  );
}

function durationDetail(duration: Report["summary"]["duration_seconds"]): string | undefined {
  if (duration.samples === 0) {
    return undefined;
  }
  const p95 = fmtDuration(duration.p95);
  return p95 === fmtDuration(duration.p50) ? "p50 and p95" : `p50, p95 ${p95}`;
}

/** Props of {@link SummaryTiles}. */
export interface SummaryTilesProps {
  report: Report;
  config: ConfigResponse | undefined;
}

/** Renders the success-rate hero beside a borderless row of summary figures. */
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
        detail={durationDetail(summary.duration_seconds)}
      />,
      <Tile
        key="turns"
        label="Turns per succeeded run"
        value={
          summary.mean_turns_succeeded === null
            ? "not recorded"
            : fmtMean(summary.mean_turns_succeeded)
        }
        detail={summary.mean_turns_succeeded === null ? undefined : "mean"}
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
  const cacheReads = statsCacheReadsText(summary, tier);
  tiles.push(
    cacheReads === null ? (
      <div key="cache-reads" className="row-span-3" />
    ) : (
      <Tile key="cache-reads" label="Cache reads" value={cacheReads} />
    ),
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
    <div className="grid grid-cols-1 gap-lg md:grid-cols-[auto_minmax(0,1fr)] md:items-end md:gap-x-xxxl">
      <div className="flex flex-col">
        <span className="font-heading text-h3 font-semibold text-ink tabular-nums">
          {fmtPercent(summary.success_rate)}
        </span>
        <span className="mt-xs text-body-md text-ink-body">
          {fmtCount(summary.succeeded)} of {plural(summary.runs, "run", "runs")} succeeded
        </span>
      </div>
      <div className="-mt-lg grid grid-cols-2 gap-x-lg md:grid-cols-3 xl:auto-cols-fr xl:grid-flow-col xl:grid-cols-none xl:grid-rows-[repeat(3,auto)]">
        {tiles}
      </div>
    </div>
  );
}
