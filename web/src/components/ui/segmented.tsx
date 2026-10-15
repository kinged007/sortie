import { Toggle } from "@base-ui/react/toggle";
import { ToggleGroup } from "@base-ui/react/toggle-group";

import { cn } from "@/lib/cn";

import type { ReactNode } from "react";

const SEGMENT =
  "inline-flex h-(--size-control) items-center rounded px-sm text-body-md font-medium whitespace-nowrap text-ink-body select-none cursor-pointer transition-colors duration-(--duration-feedback) focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-focus-ring not-data-pressed:hover:bg-[image:linear-gradient(var(--color-state-hover),var(--color-state-hover))] not-data-pressed:active:bg-[image:linear-gradient(var(--color-state-pressed),var(--color-state-pressed))] data-pressed:bg-primary data-pressed:text-on-primary";

/** One option of a {@link Segmented}. */
export interface SegmentedItem {
  value: string;
  label: ReactNode;
}

/** Props of {@link Segmented}. */
export interface SegmentedProps {
  /** Accessible name of the group. */
  label: string;
  items: readonly SegmentedItem[];
  /** The chosen item's value. */
  value: string;
  onValueChange: (value: string) => void;
  className?: string;
}

/** Renders a row of options where exactly one is chosen; pressing the chosen option keeps it chosen. */
export function Segmented({ label, items, value, onValueChange, className }: SegmentedProps) {
  function handleValueChange(next: string[]) {
    const picked = next.find((entry) => entry !== value);
    if (picked !== undefined) {
      onValueChange(picked);
    }
  }

  return (
    <ToggleGroup
      aria-label={label}
      value={[value]}
      onValueChange={handleValueChange}
      className={cn(
        "inline-flex items-center gap-xxs rounded-lg border border-line-medium bg-surface-raised p-xxs",
        className,
      )}
    >
      {items.map((item) => (
        <Toggle key={item.value} value={item.value} className={SEGMENT}>
          {item.label}
        </Toggle>
      ))}
    </ToggleGroup>
  );
}
