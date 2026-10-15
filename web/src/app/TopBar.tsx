import { Link, useMatch } from "react-router";

import { keyTip } from "@/app/key-tips";
import { modifierLabel } from "@/app/keys";
import { LiveIndicator } from "@/app/LiveIndicator";
import { useNow } from "@/app/live";
import { SearchIcon } from "@/components/icons";
import { useIssueSummary } from "@/components/use-issue-summary";
import { fmtClock, fmtDate } from "@/lib/format";

interface BreadcrumbProps {
  identifier: string;
}

function Breadcrumb({ identifier }: BreadcrumbProps) {
  const row = useIssueSummary(identifier);

  return (
    <nav aria-label="Breadcrumb" className="flex items-center gap-xs">
      <Link
        to="/"
        className="text-ink-muted transition-colors duration-(--duration-feedback) hover:text-ink hover:underline hover:underline-offset-2"
      >
        Overview
      </Link>
      <span aria-hidden="true">/</span>
      <span className="text-ink [overflow-wrap:anywhere]">{row?.displayKey ?? identifier}</span>
    </nav>
  );
}

function ServerClock() {
  const now = useNow();
  return (
    <span>
      {fmtDate(now)} · {fmtClock(now)}
    </span>
  );
}

/** Props of {@link TopBar}. */
export interface TopBarProps {
  /** True while the peek holds Overview still. */
  isHolding: boolean;
  onTogglePause: () => void;
  onOpenPalette: () => void;
}

/** Renders the bar above the page at 768 px and wider: the date and clock or the issue breadcrumb, the live indicator, and the search trigger. */
export function TopBar({ isHolding, onTogglePause, onOpenPalette }: TopBarProps) {
  const issueMatch = useMatch("/issues/:identifier");
  const identifier = issueMatch?.params.identifier;

  return (
    <div className="mb-xl flex min-h-(--size-control) items-center gap-md text-body-sm text-ink-muted max-md:hidden">
      {identifier === undefined ? <ServerClock /> : <Breadcrumb identifier={identifier} />}
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
