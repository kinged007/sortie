import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";

import { useHistoryRunsQuery } from "@/api/queries";
import { ServedText } from "@/components/ServedText";
import { StatusMark } from "@/components/StatusMark";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import {
  NOT_MEASURED,
  UNPRICED,
  runCostText,
  runTokensText,
  totalsCostText,
  totalsTokensText,
} from "@/lib/figures";
import { fmtCount, fmtDuration, fmtStamp, parseInstant, plural } from "@/lib/format";
import { issueLinkOf } from "@/lib/issue-link";
import { ruleLabel } from "@/lib/labels";
import { outcomeMark, runStatus } from "@/lib/run-status";

import type { HistoryFilter } from "@/api/client";
import type { ConfigResponse, IssueGroupResponse, RunResponse } from "@/api/types.gen";
import type { Mark } from "@/lib/run-status";
import type { ReactNode } from "react";

const CELL = "px-md py-sm align-top whitespace-nowrap tabular-nums";
const HEAD = "px-md py-sm text-left text-label-caps font-medium whitespace-nowrap text-ink-muted";
const READ_FAILED = "Run history could not be read.";

interface Columns {
  hasRule: boolean;
  hasCost: boolean;
  span: number;
}

function columnsOf(config: ConfigResponse | undefined): Columns {
  const hasRule = (config?.dispatch.rules ?? []).length > 0;
  const hasCost = config?.token_rates != null;
  return { hasRule, hasCost, span: 6 + (hasRule ? 1 : 0) + (hasCost ? 1 : 0) };
}

/** Props of {@link HistoryTableHead}. */
export interface HistoryTableHeadProps {
  config: ConfigResponse | undefined;
}

/** Renders the header row of the run table; its columns follow the workflow. */
export function HistoryTableHead({ config }: HistoryTableHeadProps) {
  const { hasRule, hasCost } = columnsOf(config);
  return (
    <thead className="border-b border-line">
      <tr>
        <th scope="col" className={HEAD}>
          Status
        </th>
        {hasRule ? (
          <th scope="col" className={HEAD}>
            Rule
          </th>
        ) : undefined}
        <th scope="col" className={HEAD}>
          Agent
        </th>
        <th scope="col" className={HEAD}>
          Duration
        </th>
        <th scope="col" className={HEAD}>
          Turns
        </th>
        <th scope="col" className={HEAD}>
          Tokens
        </th>
        {hasCost ? (
          <th scope="col" className={HEAD}>
            Est. cost
          </th>
        ) : undefined}
        <th scope="col" className={HEAD}>
          Finished
        </th>
      </tr>
    </thead>
  );
}

function groupMark(group: IssueGroupResponse): Mark | null {
  switch (group.status) {
    case "running":
      return "running";
    case "retrying":
      return "retry";
    case "budget_exhausted":
    case "parked":
    case "pending_reactions":
    case "between_stages":
      return "waiting";
    case "history":
      // Green marks only a success, and "terminal" covers a person's close too.
      return outcomeMark(group.issue_outcome?.kind);
    default:
      return null;
  }
}

function runSeconds(run: RunResponse): number | null {
  const started = parseInstant(run.started_at);
  const completed = parseInstant(run.completed_at);
  return started === null || completed === null || completed < started
    ? null
    : (completed - started) / 1000;
}

function joined(parts: readonly ReactNode[]): ReactNode {
  return parts.flatMap((part, index) => (index === 0 ? [part] : [" · ", part]));
}

function tokensPart(text: string): string {
  return text === NOT_MEASURED ? "tokens not measured" : `${text} tokens`;
}

function costPart(text: string): string {
  if (text === UNPRICED) {
    return "cost unpriced";
  }
  return text === NOT_MEASURED ? "cost not measured" : `${text} estimated`;
}

interface RunRowProps {
  run: RunResponse;
  columns: Columns;
}

function RunRow({ run, columns }: RunRowProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const link = issueLinkOf(run.issue_identifier, location);
  const status = runStatus(run.status);
  const model = run.model_name || run.configured_model;
  const agent = [run.agent_kind, model].filter((part) => part !== undefined && part !== "");
  const isModelDiverged =
    !!run.model_name && !!run.configured_model && run.model_name !== run.configured_model;
  const ruleName = run.rule_name;
  const rule = ruleLabel(ruleName);

  return (
    <tr
      onClick={() => void navigate(link.to, { state: link.state })}
      className="cursor-pointer border-t border-line hover:bg-state-hover"
    >
      <td className={CELL}>
        <Link
          to={link.to}
          state={link.state}
          onClick={(event) => event.stopPropagation()}
          className="rounded focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus-ring"
        >
          {status.mark === null ? (
            <ServedText text={status.word} />
          ) : (
            <StatusMark mark={status.mark} label={<ServedText text={status.word} />} />
          )}
        </Link>
      </td>
      {columns.hasRule ? (
        <td className={CELL}>
          <span className={cn(rule === ruleName && "font-mono text-body-sm")}>
            <ServedText text={rule} />
          </span>
          {run.stage_target ? (
            <ServedText text={` → ${run.stage_target}: ${run.stage_result ?? ""}`} />
          ) : undefined}
        </td>
      ) : undefined}
      <td
        className={CELL}
        title={isModelDiverged ? `Configured ${run.configured_model}` : undefined}
      >
        <ServedText text={agent.join(" · ")} />
      </td>
      <td className={CELL}>{fmtDuration(runSeconds(run))}</td>
      <td className={CELL}>{fmtCount(run.turns_completed)}</td>
      <td className={CELL}>{runTokensText(run)}</td>
      {columns.hasCost ? <td className={CELL}>{runCostText(run)}</td> : undefined}
      <td className={CELL}>{fmtStamp(run.completed_at)}</td>
    </tr>
  );
}

/** Props of {@link IssueGroupRows}. */
export interface IssueGroupRowsProps {
  group: IssueGroupResponse;
  filter: HistoryFilter;
  config: ConfigResponse | undefined;
}

/**
 * Renders one issue of History as a row group: a header row for the issue, its
 * newest runs, and a "Show more runs" row that swaps the served runs for pages
 * read from the run list. Mount it with a key that changes with the filter so
 * an expansion resets then.
 */
export function IssueGroupRows({ group, filter, config }: IssueGroupRowsProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const link = issueLinkOf(group.issue_identifier, location);
  const columns = columnsOf(config);
  const [isExpanded, setIsExpanded] = useState(false);
  const expansion = useHistoryRunsQuery(group.issue_id, filter, isExpanded);

  const key = group.display_identifier || group.issue_identifier;
  const title = group.issue_title || key;
  const mark = groupMark(group);
  const ending = group.status === "history" ? group.issue_outcome : null;
  const secondLine = [
    group.issue_title ? key : undefined,
    <ServedText key="status" text={group.status_text} />,
    plural(group.totals.run_count, "run", "runs"),
    tokensPart(totalsTokensText(group.totals)),
    columns.hasCost ? costPart(totalsCostText(group.totals)) : undefined,
  ].filter((part) => part !== undefined);

  const expandedRuns = expansion.data?.pages.flatMap((page) => page.runs ?? []);
  const runs = expandedRuns ?? group.runs ?? [];
  const hasMore = isExpanded
    ? expandedRuns === undefined || expansion.hasNextPage
    : group.next_cursor !== null;
  const isFailed =
    isExpanded &&
    expansion.isError &&
    (expandedRuns === undefined || expansion.isFetchNextPageError);

  function handleShowMore() {
    if (isExpanded) {
      void expansion.fetchNextPage();
    } else {
      setIsExpanded(true);
    }
  }

  return (
    <tbody className="border-b border-line last:border-b-0">
      <tr
        onClick={() => void navigate(link.to, { state: link.state })}
        className="cursor-pointer bg-surface hover:bg-state-hover"
      >
        <td colSpan={columns.span} className="px-md py-sm">
          <div className="flex items-start gap-sm">
            <span className="flex h-6 w-(--size-icon) shrink-0 items-center">
              {mark === null ? undefined : <StatusMark mark={mark} />}
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-body-md font-medium text-ink [overflow-wrap:anywhere]">
                <Link
                  to={link.to}
                  state={link.state}
                  onClick={(event) => event.stopPropagation()}
                  className="rounded focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus-ring"
                >
                  <ServedText text={title} />
                </Link>
              </p>
              <p className="text-body-sm text-ink-muted [overflow-wrap:anywhere]">
                {joined(secondLine)}
              </p>
            </div>
            {ending === null ? undefined : (
              <span className="shrink-0 text-body-sm whitespace-nowrap text-ink-muted tabular-nums">
                {fmtStamp(ending.observed_at)}
              </span>
            )}
          </div>
        </td>
      </tr>
      {runs.map((run) => (
        <RunRow key={run.run_id} run={run} columns={columns} />
      ))}
      {hasMore ? (
        <tr className="border-t border-line">
          <td colSpan={columns.span} className="px-md py-xs">
            <div className="flex items-center gap-sm">
              <Button variant="ghost" onClick={handleShowMore} disabled={expansion.isFetching}>
                Show more runs
              </Button>
              {isFailed ? (
                <span className="text-body-sm text-error">{READ_FAILED}</span>
              ) : undefined}
            </div>
          </td>
        </tr>
      ) : undefined}
    </tbody>
  );
}
