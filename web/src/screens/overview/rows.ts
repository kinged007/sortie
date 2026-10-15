import { cn } from "@/lib/cn";

/** Gives the keyboard-selection key of an attention item. */
export function attentionNavKey(id: string): string {
  return `attention:${id}`;
}

/** Gives the keyboard-selection key of an Active row. */
export function activeNavKey(issueId: string): string {
  return `active:${issueId}`;
}

/** Gives the keyboard-selection key of an Ended today row. */
export function endedNavKey(issueId: string): string {
  return `ended:${issueId}`;
}

/** Gives the classes every selectable row of Overview shares: hover, press, and the keyboard-selected bar. */
export function rowStyles(isSelected: boolean, isOpenable: boolean): string {
  return cn(
    "scroll-mt-20 scroll-mb-md transition-colors duration-(--duration-feedback)",
    isSelected
      ? "bg-item-selected shadow-[inset_2px_0_0_var(--color-line-active)]"
      : "hover:bg-item-hover active:bg-item-selected",
    isOpenable && "cursor-pointer",
  );
}

/** Gives the color of a row's second lines, raised while the row is selected so they keep their contrast. */
export function secondLineColor(isSelected: boolean): string {
  return isSelected ? "text-ink-body" : "text-ink-muted";
}
