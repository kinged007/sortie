/** The theme the reader picked; "system" follows the operating system. */
export type ThemeChoice = "system" | "light" | "dark";

const STORAGE_KEY = "sortie.theme";
const ORDER: ThemeChoice[] = ["system", "light", "dark"];

function readStored(): ThemeChoice {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return stored === "light" || stored === "dark" ? stored : "system";
  } catch {
    return "system";
  }
}

let current: ThemeChoice | undefined;

/** Returns the current choice, read from storage on first use. */
export function getTheme(): ThemeChoice {
  current ??= readStored();
  return current;
}

/**
 * Applies the choice to the root element and stores it; "system" clears both.
 * Calling `setTheme(getTheme())` before the first render applies the stored choice.
 */
export function setTheme(choice: ThemeChoice): void {
  current = choice;
  const root = document.documentElement;
  if (choice === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", choice);
  }
  try {
    if (choice === "system") {
      localStorage.removeItem(STORAGE_KEY);
    } else {
      localStorage.setItem(STORAGE_KEY, choice);
    }
  } catch {
    // The choice still holds for this page when storage throws.
  }
}

/** Moves to the next choice in the order System, Light, Dark and returns it. */
export function cycleTheme(): ThemeChoice {
  const next = ORDER[(ORDER.indexOf(getTheme()) + 1) % ORDER.length];
  setTheme(next);
  return next;
}
