import { useSearchParams } from "react-router";

import { PhaseFilter } from "@/components/PhaseFilter";
import { RuleFilter } from "@/components/RuleFilter";
import { Button } from "@/components/ui/button";
import { filterRows } from "@/lib/active-rows";
import { plural } from "@/lib/format";

import { ActiveRow } from "./ActiveRow";
import { activeNavKey } from "./rows";

import type { ConfigResponse } from "@/api/types.gen";
import type { ActiveRow as ActiveRowModel } from "@/lib/active-rows";

/** Props of {@link ActiveList}. */
export interface ActiveListProps {
  /** The served `active` rows in served order, before any filter. */
  rows: readonly ActiveRowModel[];
  /** The rows the `phase` and `rule` search parameters leave. */
  visibleRows: readonly ActiveRowModel[];
  config: ConfigResponse | undefined;
  now: number;
  /** Navigation key of the selected item, or `undefined`. */
  selectedKey: string | undefined;
  showSelection: boolean;
  onOpen: (row: ActiveRowModel) => void;
}

/**
 * Renders the Active section: the lifecycle chips, the Rule menu, and one row
 * per active issue. The rows keep the order they arrive in.
 */
export function ActiveList({
  rows,
  visibleRows,
  config,
  now,
  selectedKey,
  showSelection,
  onOpen,
}: ActiveListProps) {
  const [params, setParams] = useSearchParams();
  const ruleRows = filterRows(rows, null, params.get("rule"), config);

  function handleShowAll() {
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous);
        next.delete("phase");
        next.delete("rule");
        return next;
      },
      { replace: true },
    );
  }

  return (
    <section className="flex flex-col gap-sm">
      <div className="flex flex-wrap items-baseline gap-x-sm">
        <h2 className="font-heading text-h7 font-semibold text-ink">Active</h2>
        <span className="text-body-md text-ink-muted">
          {plural(rows.length, "issue", "issues")} · dispatch order
        </span>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-xs empty:hidden">
        <PhaseFilter rows={ruleRows} />
        <RuleFilter config={config} rows={rows} />
      </div>
      {visibleRows.length > 0 ? (
        <ul className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-canvas">
          {visibleRows.map((row) => (
            <ActiveRow
              key={row.issueId}
              row={row}
              now={now}
              isSelected={selectedKey === activeNavKey(row.issueId)}
              showSelection={showSelection}
              onOpen={onOpen}
            />
          ))}
        </ul>
      ) : (
        <div className="flex flex-wrap items-center gap-sm rounded-lg border border-line bg-canvas px-md py-lg text-body-md text-ink-body">
          {rows.length === 0 ? (
            "No issue is active."
          ) : (
            <>
              No active issue matches these filters.
              <Button onClick={handleShowAll}>Show all</Button>
            </>
          )}
        </div>
      )}
    </section>
  );
}
