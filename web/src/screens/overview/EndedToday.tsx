import { Link } from "react-router";

import { ServedText } from "@/components/ServedText";
import { StatusMark } from "@/components/StatusMark";
import { cn } from "@/lib/cn";
import { fmtClock } from "@/lib/format";
import { outcomeMark } from "@/lib/run-status";
import { rowStyles, secondLineColor } from "@/lib/rows";

import { endedNavKey } from "./rows";

import type { EndedIssue } from "@/lib/ended";

const LINK =
  "text-link underline underline-offset-2 hover:text-link-hover focus-visible:outline-2 focus-visible:outline-focus-ring active:text-link-active";

/** Why the section has no rows to list. */
export type EndedEmpty = "unavailable" | "first-run" | "none" | "loading";

/** Props of {@link EndedToday}. */
export interface EndedTodayProps {
  /** Every issue that ended today, newest ending first. */
  rows: readonly EndedIssue[];
  /** Issue ids of the rows the reader has not read; those rows carry the unread dot. */
  unreadIds: ReadonlySet<string>;
  /** Why the list is empty when it is; ignored while `rows` holds issues. */
  empty: EndedEmpty;
  /** The tally line, or `undefined` while it is unavailable. */
  tally: string | undefined;
  /** Navigation key of the selected item, or `undefined`. */
  selectedKey: string | undefined;
  showSelection: boolean;
  onOpen: (issue: EndedIssue) => void;
}

/** Renders the Ended today section: the tally line and every ending of the day. */
export function EndedToday({
  rows,
  unreadIds,
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
          <span className="text-body-md text-ink-muted wrap-anywhere">{tally}</span>
        )}
        <Link to="/history" className={cn("ml-auto text-body-md", LINK)}>
          History
        </Link>
      </div>
      {rows.length > 0 ? (
        <ul className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-canvas">
          {rows.map((issue) => (
            <EndedRow
              key={issue.issueId}
              issue={issue}
              isUnread={unreadIds.has(issue.issueId)}
              isSelected={selectedKey === endedNavKey(issue.issueId)}
              showSelection={showSelection}
              onOpen={onOpen}
            />
          ))}
        </ul>
      ) : empty === "loading" ? undefined : empty === "unavailable" ? (
        <p className="rounded-lg border border-line bg-canvas px-md py-lg text-body-md text-ink-body">
          Run history is not available.
        </p>
      ) : (
        // Holds the section's place so the page does not shift when the first ending arrives.
        <div aria-hidden="true" className="min-h-lh border border-transparent py-lg text-body-md" />
      )}
    </section>
  );
}

interface EndedRowProps {
  issue: EndedIssue;
  isUnread: boolean;
  isSelected: boolean;
  showSelection: boolean;
  onOpen: (issue: EndedIssue) => void;
}

function EndedRow({ issue, isUnread, isSelected, showSelection, onOpen }: EndedRowProps) {
  const isLook = isSelected && showSelection;
  const mark = outcomeMark(issue.outcome.kind);
  const meta = [issue.title === null ? null : issue.displayKey, issue.rule, issue.agentKind]
    .filter((part) => part !== null)
    .join(" · ");

  return (
    <li
      data-nav-key={endedNavKey(issue.issueId)}
      onClick={() => onOpen(issue)}
      className={cn(
        "grid grid-cols-[auto_auto_minmax(0,1fr)] gap-x-sm gap-y-xxs px-md py-sm md:grid-cols-[auto_auto_minmax(0,1fr)_minmax(0,1fr)_auto] md:gap-x-md wrap-anywhere",
        rowStyles(isLook, true),
      )}
    >
      <span className="flex h-5 items-center">
        <span
          aria-hidden="true"
          className={cn("size-xs rounded-full", isUnread ? "bg-primary" : "bg-transparent")}
        />
      </span>
      <span className="flex h-5 w-(--size-icon) items-center">
        {mark === null ? undefined : <StatusMark mark={mark} />}
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
