import { Link } from "react-router";

import { ApiError } from "@/api/client";
import { useIssueQuery } from "@/api/queries";
import { keyTip } from "@/app/key-tips";
import { ExternalLinkIcon } from "@/components/icons";
import { ServedText } from "@/components/ServedText";
import { StatusMark } from "@/components/StatusMark";
import { buttonStyles } from "@/components/ui/button";
import { DrawerClose, DrawerTitle } from "@/components/ui/drawer";
import { fmtClock, fmtDate, fmtDuration, parseInstant, pluralVerb } from "@/lib/format";
import { safeHttpUrl } from "@/lib/links";
import { runStatus } from "@/lib/run-status";
import { localMidnight, now } from "@/lib/server-clock";

import type { IssueHistoryResponse, NextOutcomeResponse, RunResponse } from "@/api/types.gen";

const WRAP = "[overflow-wrap:anywhere]";
const SECTION_HEADING = "text-label-caps font-medium text-ink-muted";

/** What a list already knows of an issue; shown until the issue's own response arrives. */
export interface IssueDetailRow {
  displayKey: string;
  title: string | null;
  statusTextFull: string;
}

/** Props of {@link IssueDetail}. */
export interface IssueDetailProps {
  /** The `issue_identifier` that addresses the issue. */
  identifier: string;
  row?: IssueDetailRow;
  /** Renders the drawer header with "Open page" and "Close" instead of a page heading. */
  peek?: boolean;
}

/**
 * Renders one issue: its sentence, tracker link, what happens next, and its
 * runs. The peek and the issue route share it.
 */
export function IssueDetail({ identifier, row, peek = false }: IssueDetailProps) {
  const { data, error } = useIssueQuery(identifier);
  const key = row?.displayKey ?? identifier;
  const failure = error instanceof ApiError ? error : undefined;
  const isGone = failure?.status === 404;
  const isHistoryDown = failure?.status === 503 && failure.code === "history_unavailable";
  const trackerUrl = safeHttpUrl(data?.issue.url);
  const title = data?.issue.title || row?.title;
  const sentence = data?.status_text_full || row?.statusTextFull;
  const nextOutcomes = data?.next_outcomes ?? [];

  return (
    <div className="flex flex-col gap-md p-lg max-md:p-md">
      <DetailHeader identifier={identifier} issueKey={key} state={data?.issue.state} peek={peek} />
      {isGone ? (
        <p className={`text-body-md text-ink-body ${WRAP}`}>Sortie no longer knows {key}.</p>
      ) : (
        <>
          {title ? <p className={`font-heading text-h6 text-ink ${WRAP}`}>{title}</p> : undefined}
          {sentence ? (
            <p className={`text-body-md text-ink-body ${WRAP}`}>
              <ServedText text={sentence} />
            </p>
          ) : undefined}
          {isHistoryDown ? (
            <p className="text-body-md text-ink-body">Run history could not be read.</p>
          ) : undefined}
          {trackerUrl === null ? undefined : (
            <a
              href={trackerUrl}
              target="_blank"
              rel="noopener noreferrer"
              className={buttonStyles("secondary", "self-start")}
            >
              Open in tracker
              <ExternalLinkIcon />
            </a>
          )}
          {nextOutcomes.length > 0 ? <NextOutcomes outcomes={nextOutcomes} /> : undefined}
          {data === undefined ? undefined : <Runs history={data.history} />}
        </>
      )}
    </div>
  );
}

interface DetailHeaderProps {
  identifier: string;
  issueKey: string;
  state: string | undefined;
  peek: boolean;
}

function DetailHeader({ identifier, issueKey, state, peek }: DetailHeaderProps) {
  const keyClass = "min-w-0 font-semibold text-ink tabular-nums text-h7 [overflow-wrap:anywhere]";
  return (
    <header className="flex flex-wrap items-center gap-xs">
      {peek ? (
        <DrawerTitle className={keyClass}>{issueKey}</DrawerTitle>
      ) : (
        <h1 className={keyClass}>{issueKey}</h1>
      )}
      {state ? (
        <span className="rounded border border-line-medium px-xs text-body-sm text-ink-body">
          {state}
        </span>
      ) : undefined}
      {peek ? (
        <span className="ml-auto flex items-center gap-xs">
          <Link
            to={`/issues/${encodeURIComponent(identifier)}`}
            className={buttonStyles("secondary")}
            title={keyTip("Open page", "Enter")}
          >
            Open page
          </Link>
          <DrawerClose title={keyTip("Close", "Esc")}>Close</DrawerClose>
        </span>
      ) : undefined}
    </header>
  );
}

interface NextOutcomesProps {
  outcomes: NextOutcomeResponse[];
}

function NextOutcomes({ outcomes }: NextOutcomesProps) {
  return (
    <section className="flex flex-col gap-xs">
      <h3 className={SECTION_HEADING}>What happens next</h3>
      <ul className="flex flex-col gap-xs">
        {outcomes.map((outcome) => (
          <li key={outcome.condition} className={`text-body-md text-ink-body ${WRAP}`}>
            <strong className="font-bold text-ink">
              <ServedText text={outcome.condition} />
            </strong>{" "}
            <ServedText text={outcome.text} />
          </li>
        ))}
      </ul>
    </section>
  );
}

interface RunsProps {
  history: IssueHistoryResponse | null;
}

function Runs({ history }: RunsProps) {
  if (history === null) {
    return (
      <section className="flex flex-col gap-xs">
        <h3 className={SECTION_HEADING}>Runs</h3>
        <p className="text-body-md text-ink-body">No run is recorded yet.</p>
      </section>
    );
  }
  const listed = history.runs ?? [];
  const olderRuns = pluralVerb(
    history.totals.run_count - listed.length,
    ["older run", "older runs"],
    ["is", "are"],
  );
  const startOfToday = localMidnight(now()).getTime();
  return (
    <section className="flex flex-col gap-xs">
      <h3 className={SECTION_HEADING}>Runs</h3>
      <ul className="flex flex-col gap-sm">
        {[...listed].reverse().map((run) => (
          <RunLine key={run.run_id} run={run} startOfToday={startOfToday} />
        ))}
      </ul>
      {history.next_cursor === null ? undefined : (
        <p className="text-body-md text-ink-body">
          {olderRuns.subject} {olderRuns.verb} not listed
        </p>
      )}
    </section>
  );
}

interface RunLineProps {
  run: RunResponse;
  startOfToday: number;
}

function RunLine({ run, startOfToday }: RunLineProps) {
  const started = parseInstant(run.started_at);
  const completed = parseInstant(run.completed_at);
  const isToday = started !== null && started >= startOfToday;
  const when = isToday
    ? fmtClock(run.started_at)
    : `${fmtDate(run.started_at)} ${fmtClock(run.started_at)}`;
  const { word, mark } = runStatus(run.status);
  const span = started !== null && completed !== null ? (completed - started) / 1000 : null;
  const hasHop = Boolean(run.stage_target) && Boolean(run.stage_result);

  return (
    <li className="flex flex-col gap-xxs">
      <div className="flex flex-wrap items-center gap-x-sm gap-y-xxs text-body-md">
        <span className="text-ink tabular-nums">{when}</span>
        {run.rule_name && run.rule_name !== "default" ? (
          <span className={`font-mono text-body-sm text-ink-body ${WRAP}`}>{run.rule_name}</span>
        ) : undefined}
        {mark === null ? (
          <span className="text-ink-body">{word}</span>
        ) : (
          <StatusMark mark={mark} label={word} />
        )}
        <span className="ml-auto text-ink-body tabular-nums">{fmtDuration(span)}</span>
      </div>
      {hasHop ? (
        <p className={`text-body-sm text-ink-body ${WRAP}`}>
          Hop to {run.stage_target}: {run.stage_result}
        </p>
      ) : undefined}
    </li>
  );
}
