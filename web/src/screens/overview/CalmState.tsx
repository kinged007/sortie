import { Link, useLocation } from "react-router";

import { ChartColumnIcon, ClockIcon, HistoryIcon, LoaderCircleIcon } from "@/components/icons";
import { StatusMark } from "@/components/StatusMark";
import { fmtAgo, fmtClock, fmtCount, fmtDuration, parseInstant } from "@/lib/format";
import { issueLinkOf } from "@/lib/issue-link";

import type { ReactNode } from "react";
import type { Summary } from "@/api/types.gen";
import type { IconProps } from "@/components/icons";
import type { EndedIssue } from "@/lib/ended";
import type { Mark } from "@/lib/run-status";

/** Props of {@link CalmState}. */
export interface CalmStateProps {
  /** `started_at` of every running session. */
  runningSince: readonly string[];
  /** The global agent cap, or `undefined` while the config is not served. */
  maxAgents: number | undefined;
  /** Today's ended issues, newest first. */
  ended: readonly EndedIssue[];
  /** Today's run tally, or `undefined` while it is unavailable. */
  runs: Summary | undefined;
  /** Server-clock time in epoch milliseconds. */
  now: number;
}

interface TileProps {
  label: string;
  Glyph: (props: IconProps) => ReactNode;
  value: string;
  suffix?: string;
  note?: ReactNode;
  to: string;
  state?: unknown;
}

function Tile({ label, Glyph, value, suffix, note, to, state }: TileProps) {
  return (
    <Link
      to={to}
      state={state}
      className="group flex flex-col rounded-xl border border-line bg-canvas p-md transition-colors duration-(--duration-feedback) hover:border-line-strong hover:bg-surface focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus-ring active:bg-state-pressed"
    >
      <span className="flex items-start justify-between gap-xs">
        <span className="text-body-md font-medium text-ink-muted">{label}</span>
        <Glyph
          aria-hidden="true"
          className="size-5 shrink-0 text-ink-muted transition-colors duration-(--duration-feedback) group-hover:text-ink"
        />
      </span>
      <span className="mt-xs font-heading text-h5 font-semibold text-ink tabular-nums">
        {value}
        {suffix === undefined ? undefined : (
          <span className="ml-xs font-sans text-body-md font-normal text-ink-muted">{suffix}</span>
        )}
      </span>
      {note === undefined ? undefined : (
        <span className="mt-xxs flex min-w-0 flex-wrap items-center gap-x-md gap-y-xxs text-body-md text-ink-muted">
          {note}
        </span>
      )}
    </Link>
  );
}

function Count({ mark, n, word }: { mark: Mark; n: number; word: string }) {
  return n > 0 ? <StatusMark mark={mark} label={`${fmtCount(n)} ${word}`} /> : undefined;
}

function longestRun(runningSince: readonly string[], now: number): string | undefined {
  const starts = runningSince.map(parseInstant).filter((at) => at !== null);
  return starts.length === 0
    ? undefined
    : `Longest ${fmtDuration((now - Math.min(...starts)) / 1000)}`;
}

function endedNote(ended: readonly EndedIssue[]): ReactNode {
  const count = (kind: string) => ended.filter((issue) => issue.outcome.kind === kind).length;
  return ended.length === 0 ? undefined : (
    <>
      <Count mark="succeeded" n={count("merged")} word="merged" />
      <Count mark="handed" n={count("handed_off")} word="handed off" />
      <Count mark="clear" n={count("terminal")} word="done" />
    </>
  );
}

function runsNote(runs: Summary): ReactNode {
  return runs.runs === 0 ? undefined : (
    <>
      <Count mark="succeeded" n={runs.succeeded} word="succeeded" />
      <Count mark="failed" n={runs.failed} word="failed" />
    </>
  );
}

/** Renders the calm line and the stat tiles that follow the headline while nothing needs attention; each tile opens the screen that explains its figure. */
export function CalmState({ runningSince, maxAgents, ended, runs, now }: CalmStateProps) {
  const location = useLocation();
  const last = ended.at(0);
  const lastLink = last === undefined ? undefined : issueLinkOf(last.identifier, location);

  return (
    <section aria-label="Calm" className="mb-md">
      <p className="text-body-md text-ink-body">
        Nothing is parked, held, escalated or stopped, and Sortie is healthy.
      </p>
      <div className="mt-xl grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-md">
        <Tile
          label="Agents running"
          Glyph={LoaderCircleIcon}
          value={fmtCount(runningSince.length)}
          suffix={maxAgents === undefined ? undefined : `of ${fmtCount(maxAgents)}`}
          note={longestRun(runningSince, now)}
          to="/pipeline"
        />
        <Tile
          label="Ended today"
          Glyph={HistoryIcon}
          value={fmtCount(ended.length)}
          note={endedNote(ended)}
          to="/history"
        />
        {last === undefined || lastLink === undefined ? undefined : (
          <Tile
            label="Last one ended"
            Glyph={ClockIcon}
            value={fmtClock(last.outcome.observed_at)}
            suffix={fmtAgo(last.outcome.observed_at, now)}
            note={<span className="max-w-full truncate">{last.title ?? last.displayKey}</span>}
            to={lastLink.to}
            state={lastLink.state}
          />
        )}
        {runs === undefined ? undefined : (
          <Tile
            label="Runs finished today"
            Glyph={ChartColumnIcon}
            value={fmtCount(runs.runs)}
            note={runsNote(runs)}
            to="/analytics"
          />
        )}
      </div>
    </section>
  );
}
