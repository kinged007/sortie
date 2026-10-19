import { Link, useLocation, useMatch } from "react-router";

import { keyTip } from "@/app/key-tips";
import { modifierLabel } from "@/app/keys";
import { LiveIndicator } from "@/app/LiveIndicator";
import { useNow } from "@/app/live";
import { SearchIcon } from "@/components/icons";
import { useIssueSummary } from "@/components/use-issue-summary";
import { useSessionRun } from "@/components/use-session-run";
import { fmtClock, fmtDate } from "@/lib/format";
import { backOf } from "@/lib/issue-link";

import type { ScreenEntry } from "@/app/Rail";

const FALLBACK_LABEL = "Overview";

interface BackCrumbProps {
  screens: ScreenEntry[];
}

function BackCrumb({ screens }: BackCrumbProps) {
  const back = backOf(useLocation().state);
  const backPath = back.split(/[?#]/, 1)[0];
  const label = screens.find((screen) => screen.path === backPath)?.label ?? FALLBACK_LABEL;

  return (
    <>
      <Link
        to={back}
        className="text-ink-muted transition-colors duration-(--duration-feedback) hover:text-ink hover:underline hover:underline-offset-2"
      >
        {label}
      </Link>
      <span aria-hidden="true">/</span>
    </>
  );
}

interface BreadcrumbProps {
  identifier: string;
  screens: ScreenEntry[];
}

function Breadcrumb({ identifier, screens }: BreadcrumbProps) {
  const row = useIssueSummary(identifier);

  return (
    <nav aria-label="Breadcrumb" className="flex items-center gap-xs">
      <BackCrumb screens={screens} />
      <span className="text-ink wrap-anywhere">{row?.displayKey ?? identifier}</span>
    </nav>
  );
}

interface SessionBreadcrumbProps {
  identifier: string;
  dispatchId: string;
  screens: ScreenEntry[];
}

function SessionBreadcrumb({ identifier, dispatchId, screens }: SessionBreadcrumbProps) {
  const session = useSessionRun(identifier, dispatchId, { drivePages: false });
  const back = backOf(useLocation().state);
  const isResolved = session.kind === "live" || session.kind === "finished";

  return (
    <nav aria-label="Breadcrumb" className="flex items-center gap-xs">
      <BackCrumb screens={screens} />
      <Link
        to={`/issues/${encodeURIComponent(identifier)}`}
        state={{ back }}
        className="text-ink-muted wrap-anywhere transition-colors duration-(--duration-feedback) hover:text-ink hover:underline hover:underline-offset-2"
      >
        {isResolved ? session.key : identifier}
      </Link>
      <span aria-hidden="true">/</span>
      <span className="text-ink">{isResolved ? `Session ${session.number}` : "Session"}</span>
    </nav>
  );
}

function ServerClock() {
  const now = useNow();
  return (
    <span className="flex items-center gap-xs">
      <span>{fmtDate(now)}</span> <span>{fmtClock(now)}</span>
    </span>
  );
}

/** Props of {@link TopBar}. */
export interface TopBarProps {
  screens: ScreenEntry[];
  /** True while the peek holds Overview still. */
  isHolding: boolean;
  onTogglePause: () => void;
  onOpenPalette: () => void;
}

/** Renders the bar above the page at 768 px and wider: the date and clock or the issue or session breadcrumb, the live indicator, and the search trigger. */
export function TopBar({ screens, isHolding, onTogglePause, onOpenPalette }: TopBarProps) {
  const issueMatch = useMatch("/issues/:identifier");
  const identifier = issueMatch?.params.identifier;
  const sessionParams = useMatch("/issues/:identifier/sessions/:dispatchId")?.params;

  return (
    <div className="mb-xl flex min-h-(--size-control) items-center gap-md text-body-sm text-ink-muted max-md:hidden">
      {sessionParams?.identifier !== undefined && sessionParams.dispatchId !== undefined ? (
        <SessionBreadcrumb
          identifier={sessionParams.identifier}
          dispatchId={sessionParams.dispatchId}
          screens={screens}
        />
      ) : identifier === undefined ? (
        <ServerClock />
      ) : (
        <Breadcrumb identifier={identifier} screens={screens} />
      )}
      <span className="flex-1" />
      <LiveIndicator isHolding={isHolding} onTogglePause={onTogglePause} />
      <button
        type="button"
        onClick={onOpenPalette}
        title={keyTip("Search or run a command", modifierLabel(), "K")}
        className="inline-flex h-(--size-control) cursor-pointer items-center gap-xs rounded-lg border border-line-strong bg-field px-sm whitespace-nowrap text-ink-muted transition-colors duration-(--duration-feedback) hover:border-ink-body hover:text-ink-body focus-visible:-outline-offset-1 focus-visible:outline-2 focus-visible:outline-focus-ring active:bg-state-pressed"
      >
        <SearchIcon />
        Search or run a command
      </button>
    </div>
  );
}
