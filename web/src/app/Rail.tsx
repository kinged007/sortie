import { useState } from "react";
import { NavLink, useNavigate } from "react-router";

import { useStateQuery } from "@/api/queries";
import { BrandMark } from "@/app/BrandMark";
import { keyTip } from "@/app/key-tips";
import { modifierLabel } from "@/app/keys";
import { StatusCard } from "@/app/StatusCard";
import { CircleHelpIcon, SearchIcon, XIcon } from "@/components/icons";
import { ThemeMenu } from "@/components/ThemeMenu";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { fmtVersion, pluralVerb } from "@/lib/format";

import type { StateResponse } from "@/api/types.gen";
import type { IconProps } from "@/components/icons";
import type { KeyboardEvent, ReactNode } from "react";

/**
 * One screen the shell can open. `key` is the letter pressed after `G`, `path`
 * is relative to the application's base path, and screens with the same `group`
 * sit together, with a divider between groups in ascending order. The screen at
 * path `/` is Overview, which carries the attention badge.
 */
export interface ScreenEntry {
  key: string;
  label: string;
  path: string;
  group: number;
  icon: (props: IconProps) => ReactNode;
}

const OVERVIEW_PATH = "/";

function hasCriticalItem(state: StateResponse): boolean {
  return (state.attention ?? []).some(
    (item) => item.acknowledged_at === null && item.severity === "critical",
  );
}

function groupsOf(screens: ScreenEntry[]): ScreenEntry[][] {
  const numbers = [...new Set(screens.map((screen) => screen.group))].sort((a, b) => a - b);
  return numbers.map((group) => screens.filter((screen) => screen.group === group));
}

interface AttentionBadgeProps {
  state: StateResponse;
}

function AttentionBadge({ state }: AttentionBadgeProps) {
  const count = state.counts.attention;
  if (count <= 0) {
    return undefined;
  }
  const attentionPhrase = pluralVerb(count, ["item", "items"], ["needs", "need"]);
  const tone = hasCriticalItem(state)
    ? "bg-error-fill text-on-warning-fill"
    : "bg-warning-fill text-on-warning-fill";
  return (
    <span
      title={`${attentionPhrase.subject} ${attentionPhrase.verb} attention`}
      className={cn(
        "ml-auto inline-grid h-(--size-kbd) min-w-(--size-kbd) place-items-center rounded-full px-xs text-body-sm font-medium tabular-nums",
        tone,
      )}
    >
      {count}
    </span>
  );
}

function matching(screens: ScreenEntry[], query: string): ScreenEntry[] {
  const needle = query.trim().toLowerCase();
  return needle === ""
    ? screens
    : screens.filter((screen) => screen.label.toLowerCase().includes(needle));
}

interface NavFilterProps {
  query: string;
  firstMatch: ScreenEntry | undefined;
  onQueryChange: (query: string) => void;
}

// Enter opens the first match, so typing a few letters is enough to move.
function NavFilter({ query, firstMatch, onQueryChange }: NavFilterProps) {
  const navigate = useNavigate();

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    // Enter and Escape during IME composition confirm or cancel a candidate.
    if (event.nativeEvent.isComposing) {
      return;
    }
    if (event.key === "Enter" && firstMatch !== undefined) {
      onQueryChange("");
      void navigate(firstMatch.path);
    } else if (event.key === "Escape") {
      onQueryChange("");
      event.currentTarget.blur();
    }
  }

  return (
    <label className="relative mb-md flex items-center">
      <span className="sr-only">Filter screens</span>
      <SearchIcon className="pointer-events-none absolute left-sm text-ink-muted" />
      <input
        type="text"
        placeholder="Filter"
        autoComplete="off"
        value={query}
        onChange={(event) => onQueryChange(event.target.value)}
        onKeyDown={handleKeyDown}
        className="h-(--size-control) w-full rounded-lg border border-line bg-canvas pr-xl pl-[calc(var(--spacing-sm)+var(--size-icon)+var(--spacing-xs))] text-body-md text-ink outline-none transition-colors duration-(--duration-feedback) placeholder:text-ink-muted hover:border-line-medium focus-visible:border-line-medium focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-focus-ring"
      />
      {query === "" ? undefined : (
        <button
          type="button"
          aria-label="Clear filter"
          onClick={() => onQueryChange("")}
          className="absolute right-xxs inline-grid size-6 cursor-pointer place-items-center rounded text-ink-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-focus-ring"
        >
          <XIcon />
        </button>
      )}
    </label>
  );
}

/** Props of {@link Rail}. */
export interface RailProps {
  screens: ScreenEntry[];
  onOpenPalette: () => void;
  onOpenShortcuts: () => void;
}

/** Renders the dark navigation rail: brand, screen filter, one entry per matching screen, the status card, and the foot controls. */
export function Rail({ screens, onOpenPalette, onOpenShortcuts }: RailProps) {
  const { data } = useStateQuery();
  const [query, setQuery] = useState("");
  const shown = matching(screens, query);
  const groups = groupsOf(shown);

  return (
    <aside
      data-theme="dark"
      aria-label="Sortie navigation"
      className="sticky top-0 hidden h-screen flex-col bg-surface px-sm py-lg text-ink [--ghost-hover:var(--color-item-hover-panel)] [--ghost-pressed:var(--color-item-selected-panel)] md:flex"
    >
      <div className="flex items-center gap-sm px-sm pb-md">
        <BrandMark />
        <span className="font-heading text-h7 font-semibold">Sortie</span>
        {data === undefined ? undefined : (
          <span
            title={data.version}
            className="ml-auto whitespace-nowrap text-body-sm text-ink-muted"
          >
            {fmtVersion(data.version)}
          </span>
        )}
      </div>
      <NavFilter query={query} firstMatch={shown[0]} onQueryChange={setQuery} />
      <nav aria-label="Main" className="min-h-0 overflow-y-auto">
        {shown.length === 0 ? (
          <p className="px-sm text-body-md text-ink-muted">No screens match</p>
        ) : undefined}
        {groups.map((group, index) => (
          <div key={group[0].group}>
            {index > 0 ? <div role="separator" className="mx-sm mb-md h-px bg-line" /> : undefined}
            <div className="mb-md grid gap-xxs">
              {group.map((screen) => (
                <NavLink
                  key={screen.path}
                  to={screen.path}
                  end
                  className="flex h-(--size-nav-item) items-center gap-sm rounded-lg px-sm text-ink-body transition-colors duration-(--duration-feedback) hover:bg-item-hover-panel hover:text-ink focus-visible:-outline-offset-1 focus-visible:outline-2 focus-visible:outline-focus-ring active:bg-item-selected-panel aria-[current=page]:bg-item-selected-panel aria-[current=page]:font-medium aria-[current=page]:text-ink"
                >
                  <screen.icon />
                  {screen.label}
                  {screen.path === OVERVIEW_PATH && data !== undefined ? (
                    <AttentionBadge state={data} />
                  ) : undefined}
                </NavLink>
              ))}
            </div>
          </div>
        ))}
      </nav>
      <div className="mt-auto grid gap-xxs pt-md">
        <div className="mb-sm">
          <StatusCard />
        </div>
        <Button
          variant="ghost"
          className="w-full justify-start font-regular"
          onClick={onOpenPalette}
          title={keyTip("Commands", modifierLabel(), "K")}
        >
          <SearchIcon />
          Commands
        </Button>
        <Button
          variant="ghost"
          className="w-full justify-start font-regular"
          onClick={onOpenShortcuts}
          title={keyTip("Shortcuts", "?")}
        >
          <CircleHelpIcon />
          Shortcuts
        </Button>
        <ThemeMenu />
      </div>
    </aside>
  );
}
