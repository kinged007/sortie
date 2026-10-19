import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { fmtCount, fmtDuration } from "@/lib/format";

import { COLUMN_WIDTH, CountBar } from "./GroupTable";

import type { ConfigResponse, Funnel, FunnelStage } from "@/api/types.gen";
import type { ReactNode } from "react";

const MONO = "font-mono text-body-sm";
const ENTRY_TAG =
  "inline-block border border-line-medium rounded px-xxs text-label-caps font-medium uppercase text-ink-muted";

const REASON_WORDS: Record<string, string> = {
  run_failed: "failed run",
  needs_person: "needs a person",
  budget_held: "budget held",
  operator_stopped: "stopped by an operator",
  verdict_withheld: "verdict withheld",
};

function reasonWord(reason: string): string {
  return REASON_WORDS[reason] ?? reason;
}

function handoffTargets(config: ConfigResponse | undefined): string | undefined {
  const { handoff_state: handoff, no_change_state: noChange } = config?.tracker ?? {};
  if (handoff === undefined || handoff === "") {
    return undefined;
  }
  return noChange === undefined || noChange === "" ? handoff : `${handoff} or ${noChange}`;
}

interface CountCellProps {
  value: number;
}

// A zero is muted so the figures that are not zero carry the table.
function CountCell({ value }: CountCellProps) {
  return <TableCell look={value === 0 ? "number-muted" : "number"}>{fmtCount(value)}</TableCell>;
}

interface StageRowProps {
  stage: FunnelStage;
  index: number;
  widest: number;
  targets: string | undefined;
}

function StageRow({ stage, index, widest, targets }: StageRowProps) {
  const { arrivals, outcomes } = stage;
  const reasons = stage.no_hop_reasons ?? [];
  const reasonsTitle =
    reasons.length > 0
      ? reasons.map((entry) => `${fmtCount(entry.passes)} ${reasonWord(entry.reason)}`).join(", ")
      : undefined;

  return (
    <TableRow>
      <TableCell look="number-muted">{index + 1}</TableCell>
      <TableCell>
        <span
          title={stage.rule}
          className={`inline-block truncate align-bottom ${MONO} ${stage.entry ? COLUMN_WIDTH.entryStageText : COLUMN_WIDTH.stageText}`}
        >
          {stage.rule}
        </span>
        {stage.entry ? (
          <>
            {" "}
            <span className={ENTRY_TAG}>entry</span>
          </>
        ) : undefined}
      </TableCell>
      <CountCell value={stage.runs} />
      <TableCell>
        <CountBar value={stage.entered} max={widest} />
      </TableCell>
      <TableCell look="number">
        {arrivals.label === 0 ? undefined : fmtCount(arrivals.label)}
      </TableCell>
      {stage.last ? (
        <TableCell look="number" title={targets === undefined ? undefined : `moved to ${targets}`}>
          {fmtCount(outcomes.handed_off)}
        </TableCell>
      ) : (
        <CountCell value={outcomes.advanced} />
      )}
      <CountCell value={outcomes.ceiling} />
      <CountCell value={outcomes.hop_failed} />
      <TableCell look={outcomes.no_hop === 0 ? "number-muted" : "number"} title={reasonsTitle}>
        {fmtCount(outcomes.no_hop)}
      </TableCell>
      <CountCell value={outcomes.in_stage} />
      <TableCell look={stage.median_seconds === null ? "number-muted" : "number"}>
        {fmtDuration(stage.median_seconds)}
      </TableCell>
    </TableRow>
  );
}

function headingOf(stages: readonly FunnelStage[]): ReactNode {
  const entries = stages.filter((stage) => stage.entry);
  return (
    <>
      Chain from{" "}
      {entries.map((stage, index) => (
        <span key={stage.rule}>
          {index > 0 ? " or " : undefined}
          <span className={MONO}>{stage.rule}</span>
        </span>
      ))}
    </>
  );
}

/** Props of {@link ChainFunnel}. */
export interface ChainFunnelProps {
  funnel: Funnel;
  config: ConfigResponse | undefined;
}

/** Renders one chain definition of the report: a row per stage with the passes that reached it and how each one left it. */
export function ChainFunnel({ funnel, config }: ChainFunnelProps) {
  const stages = funnel.stages ?? [];
  const widest = Math.max(1, ...stages.map((stage) => stage.entered));
  const targets = handoffTargets(config);

  return (
    <Table title={headingOf(stages)}>
      <TableHeader>
        <TableHead align="right" className={COLUMN_WIDTH.stageNumber}>
          #
        </TableHead>
        <TableHead className={COLUMN_WIDTH.stage}>Stage</TableHead>
        <TableHead
          align="right"
          className={COLUMN_WIDTH.runs}
          title="Finished runs of the rule, retries and pull request fixes included"
        >
          Runs
        </TableHead>
        <TableHead title="Passes through the stage, each counted once">Entered</TableHead>
        <TableHead
          align="right"
          title="Passes that arrived by a stage label a person put on the issue"
        >
          By label
        </TableHead>
        <TableHead
          align="right"
          title={`Passes that hopped to the next stage; on the last stage, ${targets === undefined ? "handed off" : `moved to ${targets}`}`}
        >
          Advanced
        </TableHead>
        <TableHead align="right" title="Hops refused by dispatch.max_consecutive_hops">
          Ceiling
        </TableHead>
        <TableHead align="right">Hop failed</TableHead>
        <TableHead
          align="right"
          title="The run failed, stopped for a person or at a budget, was stopped by an operator, or had its handoff withheld, so no hop was due"
        >
          No hop
        </TableHead>
        <TableHead align="right" title="Passes whose run is going or retrying now">
          In stage
        </TableHead>
        <TableHead align="right">Median</TableHead>
      </TableHeader>
      <TableBody>
        {stages.map((stage, index) => (
          <StageRow
            key={stage.rule}
            stage={stage}
            index={index}
            widest={widest}
            targets={targets}
          />
        ))}
      </TableBody>
    </Table>
  );
}
