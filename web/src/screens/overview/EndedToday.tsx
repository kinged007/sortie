import { ServedText } from "@/components/ServedText";
import { StatusMark } from "@/components/StatusMark";
import { cn } from "@/lib/cn";
import { fmtClock } from "@/lib/format";

import { endedNavKey, rowStyles, secondLineColor } from "./rows";

import type { EndedIssue } from "@/lib/ended";
import type { Mark } from "@/lib/run-status";

/** What the section says when it has no rows to list. */
export type EndedEmpty = "unavailable" | "first-run" | "none" | "all-read" | "loading";

/** Props of {@link EndedToday}. */
export interface EndedTodayProps {
  /** The ended issues the reader has not read, newest ending first. */
  rows: readonly EndedIssue[];
  /** Why the list is empty when it is; ignored while `rows` holds issues. */
  empty: EndedEmpty;
  /** The tally line, or `undefined` while it is unavailable. */
  tally: string | undefined;
  /** Navigation key of the selected item, or `undefined`. */
  selectedKey: string | undefined;
  showSelection: boolean;
  onOpen: (issue: EndedIssue) => void;
}

const EMPTY_TEXT: Record<Exclude<EndedEmpty, "loading">, string> = {
  unavailable: "Run history is not available.",
  "first-run": "Nothing has ended yet.",
  none: "Nothing has ended today.",
  "all-read": "All of today's endings are read.",
};

const OUTCOME_MARK: Partial<Record<string, Mark>> = { handed_off: "handed", merged: "succeeded" };

/** Renders the Ended today section: the tally line and the endings the reader has not read. */
export function EndedToday({
  rows,
  empty,
  tally,
  selectedKey,
  showSelection,
  onOpen,
}: EndedTodayProps) {
  return (
    <section className="flex flex-col gap-sm">
      <div className="flex flex-wrap items-baseline gap-x-sm">
        <h2 className="font-heading text-h7 font-semibold text-ink">Ended today</h2>
        {tally === undefined ? undefined : (
          <span className="text-body-md text-ink-muted [overflow-wrap:anywhere]">{tally}</span>
        )}
      </div>
      {rows.length > 0 ? (
        <ul className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-canvas">
          {rows.map((issue) => (
            <EndedRow
              key={issue.issueId}
              issue={issue}
              isSelected={selectedKey === endedNavKey(issue.issueId)}
              showSelection={showSelection}
              onOpen={onOpen}
            />
          ))}
        </ul>
      ) : empty === "loading" ? undefined : (
        <p className="rounded-lg border border-line bg-canvas px-md py-lg text-body-md text-ink-body">
          {EMPTY_TEXT[empty]}
        </p>
      )}
    </section>
  );
}

interface EndedRowProps {
  issue: EndedIssue;
  isSelected: boolean;
  showSelection: boolean;
  onOpen: (issue: EndedIssue) => void;
}

function EndedRow({ issue, isSelected, showSelection, onOpen }: EndedRowProps) {
  const isLook = isSelected && showSelection;
  const mark = OUTCOME_MARK[issue.outcome.kind];
  const meta = [issue.title === null ? null : issue.displayKey, issue.rule, issue.agentKind]
    .filter((part) => part !== null)
    .join(" · ");

  return (
    <li
      data-nav-key={endedNavKey(issue.issueId)}
      onClick={() => onOpen(issue)}
      className={cn(
        "grid grid-cols-[auto_auto_minmax(0,1fr)] gap-x-sm gap-y-xxs px-md py-sm md:grid-cols-[auto_auto_minmax(0,1fr)_minmax(0,1fr)_auto] md:gap-x-md [overflow-wrap:anywhere]",
        rowStyles(isLook, true),
      )}
    >
      <span className="flex h-5 items-center">
        <span aria-hidden="true" className="size-xs rounded-full bg-primary" />
      </span>
      <span className="flex h-5 w-(--size-icon) items-center">
        {mark === undefined ? undefined : <StatusMark mark={mark} />}
      </span>
      <div className="min-w-0">
        <p className="text-body-md font-medium text-ink">{issue.title ?? issue.displayKey}</p>
        {meta === "" ? undefined : (
          <p className={cn("text-body-sm tabular-nums", secondLineColor(isLook))}>{meta}</p>
        )}
      </div>
      <p className="text-body-md text-ink-body max-md:col-start-3">
        <ServedText text={issue.outcome.status_text} />
      </p>
      <p className="text-body-md text-ink-body tabular-nums max-md:col-start-3 md:text-right">
        {fmtClock(issue.outcome.observed_at)}
      </p>
    </li>
  );
}
