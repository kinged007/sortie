import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router";

import { useIssueLookupQuery, useStateQuery } from "@/api/queries";
import { Kbd } from "@/app/Kbd";
import { useLive } from "@/app/live";
import { ServedText } from "@/components/ServedText";
import {
  Autocomplete,
  AutocompleteCollection,
  AutocompleteEmpty,
  AutocompleteGroup,
  AutocompleteGroupLabel,
  AutocompleteInput,
  AutocompleteItem,
  AutocompleteList,
  useAutocompleteFilter,
} from "@/components/ui/autocomplete";
import { Dialog, DialogPopup } from "@/components/ui/dialog";
import { issueLinkOf } from "@/lib/issue-link";
import { setTheme } from "@/lib/theme";

import type { ScreenEntry } from "@/app/Rail";
import type { ActiveIssueResponse, IssueGroupResponse } from "@/api/types.gen";
import type { AutocompleteGroupData } from "@/components/ui/autocomplete";
import type { ThemeChoice } from "@/lib/theme";

interface Command {
  value: string;
  label: string;
  keys: string[];
  run: () => void;
}

interface IssueEntry {
  value: string;
  label: string;
  identifier: string;
  statusText: string;
}

type PaletteItem = Command | IssueEntry;

const ISSUES_GROUP = "Issues";
const ACTIVE_ENTRY_CAP = 8;
const FINISHED_ENTRY_CAP = 5;
const LOOKUP_DEBOUNCE_MS = 250;
const LOOKUP_FAILED = "Run history could not be read.";

function isIssueEntry(item: PaletteItem): item is IssueEntry {
  return "identifier" in item;
}

function issueEntryOf(issue: ActiveIssueResponse | IssueGroupResponse): IssueEntry {
  const key = issue.display_identifier || issue.issue_identifier;
  return {
    value: `issue:${issue.issue_id}`,
    label: issue.issue_title ? `${key} ${issue.issue_title}` : key,
    identifier: issue.issue_identifier,
    statusText: issue.status_text,
  };
}

function matchesActive(issue: ActiveIssueResponse, query: string): boolean {
  const needle = query.toLowerCase();
  const key = issue.display_identifier || issue.issue_identifier;
  return (
    key.toLowerCase().includes(needle) || (issue.issue_title ?? "").toLowerCase().includes(needle)
  );
}

function useDebounced(value: string, delayMs: number): string {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}

const THEME_COMMANDS: { choice: ThemeChoice; label: string }[] = [
  { choice: "system", label: "Theme: System" },
  { choice: "light", label: "Theme: Light" },
  { choice: "dark", label: "Theme: Dark" },
];

/** Props of {@link CommandPalette}. */
export interface CommandPaletteProps {
  screens: ScreenEntry[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onRefresh: () => void;
  onTogglePause: () => void;
  onMarkAllRead: () => void;
  onOpenShortcuts: () => void;
}

/** Renders the command palette: a dialog with a search field over "Go to" entries for the registered screens and the global commands. */
export function CommandPalette({
  screens,
  open,
  onOpenChange,
  onRefresh,
  onTogglePause,
  onMarkAllRead,
  onOpenShortcuts,
}: CommandPaletteProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const { paused } = useLive();
  const { contains } = useAutocompleteFilter();
  const [query, setQuery] = useState("");
  const issueQuery = query.trim();
  const state = useStateQuery().data;
  const lookup = useIssueLookupQuery(useDebounced(open ? issueQuery : "", LOOKUP_DEBOUNCE_MS));

  const commands: Command[] = [
    ...screens.map((screen) => ({
      value: `go:${screen.path}`,
      label: `Go to ${screen.label}`,
      keys: ["G", screen.key.toUpperCase()],
      run: () => void navigate(screen.path),
    })),
    { value: "refresh", label: "Refresh now", keys: ["R"], run: onRefresh },
    {
      value: "pause",
      label: paused ? "Resume live updates" : "Pause live updates",
      keys: ["P"],
      run: onTogglePause,
    },
    { value: "read", label: "Mark everything read", keys: ["Shift", "U"], run: onMarkAllRead },
    ...THEME_COMMANDS.map(({ choice, label }) => ({
      value: `theme:${choice}`,
      label,
      keys: ["T"],
      run: () => setTheme(choice),
    })),
    { value: "shortcuts", label: "Keyboard shortcuts", keys: ["?"], run: onOpenShortcuts },
  ];

  const activeIssues =
    issueQuery === ""
      ? []
      : (state?.active ?? [])
          .filter((issue) => matchesActive(issue, issueQuery))
          .slice(0, ACTIVE_ENTRY_CAP);
  const activeIds = new Set(activeIssues.map((issue) => issue.issue_id));
  const finishedIssues =
    issueQuery === ""
      ? []
      : (lookup.data ?? [])
          .filter((group) => !activeIds.has(group.issue_id))
          .slice(0, FINISHED_ENTRY_CAP);
  const issueEntries = [...activeIssues, ...finishedIssues].map(issueEntryOf);
  const groups: AutocompleteGroupData<PaletteItem>[] = [
    { value: "Commands", items: commands },
    ...(issueEntries.length === 0 ? [] : [{ value: ISSUES_GROUP, items: issueEntries }]),
  ];

  const handleRun = (command: Command) => {
    onOpenChange(false);
    command.run();
  };

  const handleOpenIssue = (entry: IssueEntry) => {
    const link = issueLinkOf(entry.identifier, location);
    onOpenChange(false);
    void navigate(link.to, { state: link.state });
  };

  // Issue entries are already matched by key or title, or by the server, so
  // the query must not narrow them again.
  const handleFilter = (
    item: PaletteItem,
    text: string,
    itemToString?: (item: PaletteItem) => string,
  ) => isIssueEntry(item) || contains(item, text, itemToString);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup aria-label="Command palette">
        <Autocomplete items={groups} value={query} onValueChange={setQuery} filter={handleFilter}>
          <AutocompleteInput aria-label="Search commands" placeholder="Search or run a command" />
          <AutocompleteList>
            {(group: AutocompleteGroupData<PaletteItem>) => (
              <AutocompleteGroup key={group.value} items={group.items}>
                {group.value === ISSUES_GROUP ? (
                  <AutocompleteGroupLabel>{ISSUES_GROUP}</AutocompleteGroupLabel>
                ) : undefined}
                <AutocompleteCollection>
                  {(item: PaletteItem) =>
                    isIssueEntry(item) ? (
                      <AutocompleteItem
                        key={item.value}
                        value={item}
                        onClick={() => handleOpenIssue(item)}
                        className="py-xs"
                      >
                        <span className="flex min-w-0 flex-col">
                          <span className="[overflow-wrap:anywhere]">{item.label}</span>
                          <span className="text-body-sm text-ink-muted [overflow-wrap:anywhere]">
                            <ServedText text={item.statusText} />
                          </span>
                        </span>
                      </AutocompleteItem>
                    ) : (
                      <AutocompleteItem
                        key={item.value}
                        value={item}
                        onClick={() => handleRun(item)}
                      >
                        <span>{item.label}</span>
                        <span className="flex gap-xxs">
                          {item.keys.map((key) => (
                            <Kbd key={key}>{key}</Kbd>
                          ))}
                        </span>
                      </AutocompleteItem>
                    )
                  }
                </AutocompleteCollection>
              </AutocompleteGroup>
            )}
          </AutocompleteList>
          {issueQuery !== "" && lookup.isError ? (
            <p className="px-md py-xs text-body-sm text-ink-muted">{LOOKUP_FAILED}</p>
          ) : undefined}
          <AutocompleteEmpty>{`Nothing matches "${query}".`}</AutocompleteEmpty>
        </Autocomplete>
      </DialogPopup>
    </Dialog>
  );
}
