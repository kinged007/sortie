import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  statsCostText,
  statsShortfall,
  statsTokensShortfall,
  statsTokensText,
} from "@/lib/figures";
import { fmtCount, fmtDuration, fmtMean, fmtPercent } from "@/lib/format";

import type { Duration, Group } from "@/api/types.gen";
import type { ReactNode } from "react";

const PAGE_ROWS = 20;
const CELL = "px-md py-sm whitespace-nowrap tabular-nums";
const HEAD = "px-md py-sm text-left text-label-caps font-medium whitespace-nowrap text-ink-muted";

function durationText(duration: Duration): string {
  if (duration.samples === 0) {
    return "unknown";
  }
  const low = fmtDuration(duration.p50);
  const high = fmtDuration(duration.p95);
  return low === high ? low : `${low} to ${high}`;
}

/** One name column of a {@link GroupTable}. */
export interface NameColumn<Row> {
  header: string;
  cell: (row: Row) => ReactNode;
}

/** Props of {@link GroupTable}. */
export interface GroupTableProps<Row extends Group> {
  title: string;
  nameColumns: readonly NameColumn<Row>[];
  rows: readonly Row[];
  /** The report's `schema_tier`. */
  tier: string;
  /** Whether the workflow configures `token_rates`, which adds the cost column. */
  hasRates: boolean;
  /** `share` swaps the success column for the share of runs, as a breakdown by run status needs. */
  measure?: "success" | "share";
}

/**
 * Renders one breakdown of a report: name columns, then the figure columns,
 * 20 rows at a time. The shown count lives in state, so mount the table with a
 * key that changes with the range.
 */
export function GroupTable<Row extends Group>({
  title,
  nameColumns,
  rows,
  tier,
  hasRates,
  measure = "success",
}: GroupTableProps<Row>) {
  const [shown, setShown] = useState(PAGE_ROWS);
  const body = useRef<HTMLTableSectionElement>(null);
  const focusRow = useRef<number | null>(null);

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
      <h2 className="font-heading text-h7 font-semibold text-ink">{title}</h2>
      <div className="max-w-full overflow-x-auto rounded-lg border border-line bg-canvas">
        <table className="w-full border-collapse text-body-md text-ink-body">
          <thead className="border-b border-line">
            <tr>
              {nameColumns.map((column) => (
                <th key={column.header} scope="col" className={HEAD}>
                  {column.header}
                </th>
              ))}
              <th scope="col" className={HEAD}>
                Runs
              </th>
              <th scope="col" className={HEAD}>
                {measure === "share" ? "Share" : "Success"}
              </th>
              <th scope="col" className={HEAD} title="p50 to p95">
                Duration
              </th>
              <th scope="col" className={HEAD}>
                Mean turns
              </th>
              <th scope="col" className={HEAD}>
                Tokens
              </th>
              {hasRates ? (
                <th scope="col" className={HEAD}>
                  Est. cost
                </th>
              ) : undefined}
            </tr>
          </thead>
          <tbody ref={body}>
            {rows.slice(0, shown).map((row, index) => (
              <tr
                key={index}
                tabIndex={-1}
                className="border-t border-line outline-focus-ring first:border-t-0 focus-visible:outline-2 focus-visible:-outline-offset-2"
              >
                {nameColumns.map((column) => (
                  <td key={column.header} className={CELL}>
                    {column.cell(row)}
                  </td>
                ))}
                <td className={CELL}>{fmtCount(row.runs)}</td>
                <td className={CELL}>
                  {fmtPercent(measure === "share" ? row.share : row.success_rate)}
                </td>
                <td className={CELL}>{durationText(row.duration_seconds)}</td>
                <td className={CELL}>
                  {row.mean_turns === null ? "not recorded" : fmtMean(row.mean_turns)}
                </td>
                <td className={CELL} title={statsTokensShortfall(row) ?? undefined}>
                  {statsTokensText(row, tier)}
                </td>
                {hasRates ? (
                  <td className={CELL} title={statsShortfall(row) ?? undefined}>
                    {statsCostText(row, tier)}
                  </td>
                ) : undefined}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {shown < rows.length ? (
        <div>
          <Button onClick={handleShowMore}>Show more</Button>
        </div>
      ) : undefined}
    </section>
  );
}
