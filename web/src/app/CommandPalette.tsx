import { useState } from "react";
import { useNavigate } from "react-router";

import { Kbd } from "@/app/Kbd";
import { useLive } from "@/app/live";
import {
  Autocomplete,
  AutocompleteEmpty,
  AutocompleteInput,
  AutocompleteItem,
  AutocompleteList,
} from "@/components/ui/autocomplete";
import { Dialog, DialogPopup } from "@/components/ui/dialog";
import { setTheme } from "@/lib/theme";

import type { ScreenEntry } from "@/app/Rail";
import type { ThemeChoice } from "@/lib/theme";

interface Command {
  value: string;
  label: string;
  keys: string[];
  run: () => void;
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
  const { paused } = useLive();
  const [query, setQuery] = useState("");

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

  const handleRun = (command: Command) => {
    onOpenChange(false);
    command.run();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup aria-label="Command palette">
        <Autocomplete items={commands} value={query} onValueChange={setQuery}>
          <AutocompleteInput aria-label="Search commands" placeholder="Search or run a command" />
          <AutocompleteList>
            {(command: Command) => (
              <AutocompleteItem
                key={command.value}
                value={command}
                onClick={() => handleRun(command)}
              >
                <span>{command.label}</span>
                <span className="flex gap-xxs">
                  {command.keys.map((key) => (
                    <Kbd key={key}>{key}</Kbd>
                  ))}
                </span>
              </AutocompleteItem>
            )}
          </AutocompleteList>
          <AutocompleteEmpty>{`Nothing matches "${query}".`}</AutocompleteEmpty>
        </Autocomplete>
      </DialogPopup>
    </Dialog>
  );
}
