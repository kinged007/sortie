import { useSearchParams } from "react-router";

import { ApiError } from "@/api/client";
import { useStateQuery, useStatsQuery } from "@/api/queries";
import { ServedText } from "@/components/ServedText";
import { StatusMark } from "@/components/StatusMark";
import { Segmented } from "@/components/ui/segmented";
import { cn } from "@/lib/cn";
import { fmtClock, fmtDate } from "@/lib/format";
import { agentLabel, modelLabel, ruleLabel, startLabel } from "@/lib/labels";
import { runStatus } from "@/lib/run-status";

import { GroupTable } from "./GroupTable";
import { SummaryTiles } from "./SummaryTiles";

import type { AnalyticsRange } from "@/api/queries";
import type { Group, Report } from "@/api/types.gen";
import type { NameColumn } from "./GroupTable";

const RANGE_PARAM = "range";
const DEFAULT_RANGE: AnalyticsRange = "7d";
const RANGE_ITEMS = [
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "all", label: "Since first run" },
];
const MESSAGE = "text-body-md text-ink-body";

function rangeOf(value: string | null): AnalyticsRange {
  return value === "24h" || value === "7d" || value === "all" ? value : DEFAULT_RANGE;
}

function isUnavailable(error: unknown): boolean {
  return error instanceof ApiError && error.status === 503 && error.code === "history_unavailable";
}

interface NameTextProps {
  name: string;
  label: string;
}

// A name the client has no wording for reads as served, in the monospace face.
function NameText({ name, label }: NameTextProps) {
  return (
    <span className={cn(label === name && "font-mono text-body-sm")}>
      <ServedText text={label} />
    </span>
  );
}

const RULE_COLUMN: NameColumn<Group> = {
  header: "Rule",
  cell: (row) => <NameText name={row.name} label={ruleLabel(row.name)} />,
};
const MODEL_COLUMN: NameColumn<Group> = {
  header: "Model",
  cell: (row) => <NameText name={row.name} label={modelLabel(row.name)} />,
};
const AGENT_COLUMN: NameColumn<Group> = {
  header: "Agent",
  cell: (row) => <NameText name={row.name} label={agentLabel(row.name)} />,
};
const STATUS_COLUMN: NameColumn<Group> = {
  header: "Status",
  cell: (row) => {
    const { word, mark } = runStatus(row.name);
    return mark === null ? (
      <ServedText text={word} />
    ) : (
      <StatusMark mark={mark} label={<ServedText text={word} />} />
    );
  },
};
const TRIGGER_COLUMN: NameColumn<Group> = {
  header: "Trigger",
  cell: (row) => <ServedText text={startLabel(row.name, "trigger")} />,
};
const ORIGIN_COLUMN: NameColumn<Group> = {
  header: "Started by",
  cell: (row) => <ServedText text={startLabel(row.name, "origin")} />,
};

interface BreakdownsProps {
  report: Report;
  hasRules: boolean;
  hasRates: boolean;
}

function Breakdowns({ report, hasRules, hasRates }: BreakdownsProps) {
  const tier = report.schema_tier;
  const common = { tier, hasRates };
  const byRule = report.by_rule ?? [];
  const byModel = report.by_model ?? [];
  const byAdapter = report.by_adapter ?? [];
  const byStatus = report.by_status ?? [];
  const byTrigger = report.by_trigger ?? [];
  const byOrigin = report.by_origin ?? [];

  return (
    <>
      {hasRules && byRule.length > 0 ? (
        <GroupTable title="By rule" nameColumns={[RULE_COLUMN]} rows={byRule} {...common} />
      ) : undefined}
      {byModel.length > 0 ? (
        <GroupTable
          title="By configured model"
          nameColumns={[MODEL_COLUMN]}
          rows={byModel}
          {...common}
        />
      ) : undefined}
      {byAdapter.length > 0 ? (
        <GroupTable title="By agent" nameColumns={[AGENT_COLUMN]} rows={byAdapter} {...common} />
      ) : undefined}
      {byStatus.length > 0 ? (
        <GroupTable
          title="By run status"
          nameColumns={[STATUS_COLUMN]}
          rows={byStatus}
          measure="share"
          {...common}
        />
      ) : undefined}
      {byTrigger.length > 0 ? (
        <GroupTable
          title="By retry trigger"
          nameColumns={[TRIGGER_COLUMN]}
          rows={byTrigger}
          {...common}
        />
      ) : undefined}
      {byOrigin.length > 0 ? (
        <GroupTable
          title="Why runs started"
          nameColumns={[ORIGIN_COLUMN]}
          rows={byOrigin}
          {...common}
        />
      ) : undefined}
    </>
  );
}

/** Renders Analytics: the run statistics report of a range with its summary and breakdowns, every figure as the report serves it. */
export function AnalyticsScreen() {
  const { data: state } = useStateQuery();
  const config = state?.config;
  const [params, setParams] = useSearchParams();
  const range = rangeOf(params.get(RANGE_PARAM));
  const stats = useStatsQuery(range);
  const report = stats.data;

  function handleRangeChange(value: string) {
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous);
        next.set(RANGE_PARAM, value);
        return next;
      },
      { replace: true },
    );
  }

  return (
    <div className="flex flex-col gap-lg">
      <div className="flex flex-wrap items-center gap-x-md gap-y-sm">
        <h1 className="font-heading text-h4 font-semibold text-ink">Analytics</h1>
        <Segmented
          label="Range"
          items={RANGE_ITEMS}
          value={range}
          onValueChange={handleRangeChange}
        />
        {report?.since == null ? undefined : (
          <span className="text-body-md text-ink-muted" title={report.since}>
            Since {fmtDate(report.since)}, {fmtClock(report.since)}
          </span>
        )}
      </div>
      {report === undefined ? (
        stats.isError ? (
          <p className={MESSAGE}>
            {isUnavailable(stats.error)
              ? "Run history is not available."
              : "Run statistics could not be read."}
          </p>
        ) : (
          <p className={MESSAGE}>Loading</p>
        )
      ) : (
        <>
          {(report.warnings ?? []).map((warning, index) => (
            <p
              key={index}
              className="rounded-lg bg-warning-container px-md py-sm text-body-md text-ink"
            >
              <ServedText text={warning} />
            </p>
          ))}
          {report.summary.runs === 0 ? (
            <p className={MESSAGE}>
              {range === "all" ? "No run has finished yet." : "No run finished in this range."}
            </p>
          ) : (
            <div key={range} className="flex flex-col gap-xl">
              <SummaryTiles report={report} config={config} />
              <Breakdowns
                report={report}
                hasRules={(config?.dispatch.rules ?? []).length > 0}
                hasRates={config?.token_rates != null}
              />
            </div>
          )}
        </>
      )}
    </div>
  );
}
