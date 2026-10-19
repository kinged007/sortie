import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  SortableHead,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useTableSort } from "@/components/use-table-sort";
import {
  statsCacheReadsText,
  statsCostText,
  statsShortfall,
  statsTokensShortfall,
  statsTokensText,
} from "@/lib/figures";
import { fmtCount, fmtDuration, fmtMean, fmtPercent } from "@/lib/format";

import type { Duration, Group } from "@/api/types.gen";
import type { SortColumn } from "@/components/use-table-sort";
import type { ReactNode } from "react";

const PAGE_ROWS = 20;

/**
 * Column widths every Analytics table shares, so the columns line up from table
 * to table; Duration takes the rest. A chain table splits the name width into
 * its stage number and stage name, so its Runs column lines up too.
 */
export const COLUMN_WIDTH = {
  name: "w-[240px]",
  nameText: "max-w-[208px]",
  stageNumber: "w-[48px]",
  stage: "w-[192px]",
  stageText: "max-w-[160px]",
  entryStageText: "max-w-[96px]",
  runs: "w-[140px]",
  measure: "w-[124px]",
  turns: "w-[120px]",
  figure: "w-[156px]",
} as const;

function durationText(duration: Duration): string {
  if (duration.samples === 0) {
    return "unknown";
  }
  const low = fmtDuration(duration.p50);
  const high = fmtDuration(duration.p95);
  return low === high ? low : `${low} to ${high}`;
}

/** Props of {@link CountBar}. */
export interface CountBarProps {
  value: number;
  /** The largest value of the table. */
  max: number;
}

/** Renders a count after a bar as wide as its share of the table's largest count. */
export function CountBar({ value, max }: CountBarProps) {
  return (
    <span className="inline-grid grid-cols-[48px_auto] items-center gap-xs">
      <span aria-hidden="true" className="block h-2 bg-transparent">
        <span
          className="block h-full rounded-r bg-viz-1"
          style={{ width: `${(value / Math.max(max, 1)) * 100}%` }}
        />
      </span>
      {fmtCount(value)}
    </span>
  );
}

interface RateBarProps {
  ratio: number;
}

function RateBar({ ratio }: RateBarProps) {
  return (
    <span className="inline-grid grid-cols-[40px_auto] items-center gap-xs">
      <span aria-hidden="true" className="block h-1 overflow-hidden rounded-full bg-viz-1-track">
        <span
          className="block h-full rounded-full bg-viz-1"
          style={{ width: `${Math.min(1, Math.max(0, ratio)) * 100}%` }}
        />
      </span>
      {fmtPercent(ratio)}
    </span>
  );
}

const RANGE_WIDTH = 64;
const RANGE_INSET = 2;

interface DurationRangeProps {
  duration: Duration;
  /** The largest p95 of the table. */
  maxP95: number;
}

function DurationRange({ duration, maxP95 }: DurationRangeProps) {
  const { p50, p95, samples } = duration;
  const x = (seconds: number) =>
    RANGE_INSET + (maxP95 > 0 ? seconds / maxP95 : 0) * (RANGE_WIDTH - 2 * RANGE_INSET);

  return (
    <span className="inline-grid grid-cols-[64px_auto] items-center gap-xs">
      <span aria-hidden="true" className="block h-3">
        {samples > 0 && p50 !== null && p95 !== null ? (
          <svg width={RANGE_WIDTH} height={12} className="block overflow-visible">
            <line
              x1={x(p50)}
              x2={x(p95)}
              y1={6}
              y2={6}
              strokeWidth={2}
              strokeLinecap="round"
              className="stroke-ink-muted"
            />
            <circle cx={x(p50)} cy={6} r={4} strokeWidth={2} className="fill-ink stroke-canvas" />
          </svg>
        ) : undefined}
      </span>
      <span className="text-body-sm text-ink-muted">{durationText(duration)}</span>
    </span>
  );
}

function tokensTitle(row: Group, tier: string): string | undefined {
  const cacheReads = statsCacheReadsText(row, tier);
  const parts = [
    cacheReads === null ? null : `${cacheReads} cache reads`,
    statsTokensShortfall(row),
  ].filter((part) => part !== null);
  return parts.length === 0 ? undefined : parts.join(", ");
}

/** One name column of a {@link GroupTable}. */
export interface NameColumn<Row> {
  header: string;
  cell: (row: Row) => ReactNode;
  /** The words the cell shows, which the column sorts by. */
  sortText: (row: Row) => string;
}

/** Props of {@link GroupTable}. */
export interface GroupTableProps<Row extends Group> {
  title: string;
  /** The address param that keeps this table's sort. */
  sortParam: string;
  nameColumns: readonly NameColumn<Row>[];
  rows: readonly Row[];
  /** The report's `schema_tier`. */
  tier: string;
  /** Whether the workflow configures `token_rates`, which adds the cost column. */
  hasRates: boolean;
  /** `share` swaps the success column for the share of runs, as a breakdown by run status needs. */
  measure?: "success" | "share";
}

function sortColumns<Row extends Group>(
  nameColumns: readonly NameColumn<Row>[],
  tier: string,
  measure: "success" | "share",
): SortColumn<Row>[] {
  const isRecorded = tier !== "base";
  return [
    ...nameColumns.map((column, index) => ({ id: `name${index}`, value: column.sortText })),
    { id: "runs", value: (row: Row) => row.runs },
    {
      id: measure,
      value: (row: Row) => (measure === "share" ? row.share : row.success_rate),
    },
    {
      id: "duration",
      value: (row: Row) => (row.duration_seconds.samples === 0 ? null : row.duration_seconds.p50),
    },
    { id: "turns", value: (row: Row) => row.mean_turns },
    {
      id: "tokens",
      value: (row: Row) => (isRecorded && row.tokens !== null ? row.tokens.uncached : null),
    },
    { id: "cost", value: (row: Row) => (isRecorded ? row.cost_usd : null) },
  ];
}

/**
 * Renders one breakdown of a report: name columns, then the figure columns,
 * 20 rows at a time, sortable by any column. The shown count lives in state, so
 * mount the table with a key that changes with the range.
 */
export function GroupTable<Row extends Group>({
  title,
  sortParam,
  nameColumns,
  rows,
  tier,
  hasRates,
  measure = "success",
}: GroupTableProps<Row>) {
  const [shown, setShown] = useState(PAGE_ROWS);
  const maxRuns = Math.max(1, ...rows.map((row) => row.runs));
  const maxP95 = Math.max(
    0,
    ...rows.map((row) => (row.duration_seconds.samples > 0 ? (row.duration_seconds.p95 ?? 0) : 0)),
  );
  const body = useRef<HTMLTableSectionElement>(null);
  const focusRow = useRef<number | null>(null);
  const { rows: ordered, sort } = useTableSort(
    rows,
    sortColumns(nameColumns, tier, measure),
    sortParam,
  );

  useEffect(() => {
    if (focusRow.current !== null) {
      body.current?.rows[focusRow.current]?.focus();
      focusRow.current = null;
    }
  }, [shown]);

  function handleShowMore() {
    const next = shown + PAGE_ROWS;
    // The button leaves with the last rows, so focus moves to the first row added.
    if (next >= rows.length) {
      focusRow.current = shown;
    }
    setShown(next);
  }

  return (
    <section className="flex flex-col gap-sm">
      <Table title={title} className={hasRates ? "table-fixed min-w-287" : "table-fixed min-w-248"}>
        <TableHeader>
          {nameColumns.map((column, index) => (
            <SortableHead
              key={column.header}
              columnId={`name${index}`}
              sort={sort}
              className={COLUMN_WIDTH.name}
            >
              {column.header}
            </SortableHead>
          ))}
          <SortableHead columnId="runs" sort={sort} className={COLUMN_WIDTH.runs}>
            Runs
          </SortableHead>
          <SortableHead columnId={measure} sort={sort} className={COLUMN_WIDTH.measure}>
            {measure === "share" ? "Share" : "Success"}
          </SortableHead>
          <SortableHead columnId="duration" sort={sort} title="p50 to p95">
            Duration
          </SortableHead>
          <SortableHead columnId="turns" sort={sort} align="right" className={COLUMN_WIDTH.turns}>
            Mean turns
          </SortableHead>
          <SortableHead
            columnId="tokens"
            sort={sort}
            align="right"
            title="Cache reads not included"
            className={COLUMN_WIDTH.figure}
          >
            Tokens
          </SortableHead>
          {hasRates ? (
            <SortableHead columnId="cost" sort={sort} align="right" className={COLUMN_WIDTH.figure}>
              Est. cost
            </SortableHead>
          ) : undefined}
        </TableHeader>
        <TableBody ref={body}>
          {ordered.slice(0, shown).map((row, index) => (
            <TableRow key={index} tabIndex={-1}>
              {nameColumns.map((column) => (
                <TableCell key={column.header}>
                  <span
                    title={column.sortText(row)}
                    className={`block truncate ${COLUMN_WIDTH.nameText}`}
                  >
                    {column.cell(row)}
                  </span>
                </TableCell>
              ))}
              <TableCell>
                <CountBar value={row.runs} max={maxRuns} />
              </TableCell>
              <TableCell>
                <RateBar ratio={measure === "share" ? row.share : row.success_rate} />
              </TableCell>
              <TableCell>
                <DurationRange duration={row.duration_seconds} maxP95={maxP95} />
              </TableCell>
              <TableCell look="number">
                {row.mean_turns === null ? "not recorded" : fmtMean(row.mean_turns)}
              </TableCell>
              <TableCell look="number" title={tokensTitle(row, tier)}>
                {statsTokensText(row, tier)}
              </TableCell>
              {hasRates ? (
                <TableCell look="number" title={statsShortfall(row) ?? undefined}>
                  {statsCostText(row, tier)}
                </TableCell>
              ) : undefined}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {shown < rows.length ? (
        <div>
          <Button onClick={handleShowMore}>Show more</Button>
        </div>
      ) : undefined}
    </section>
  );
}
