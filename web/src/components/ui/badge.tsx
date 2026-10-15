import type { ReactNode } from "react";

/** Props of {@link Badge}. */
export interface BadgeProps {
  children: ReactNode;
}

/** Renders a neutral chip, such as a tracker label, as the design system's `chip` token. */
export function Badge({ children }: BadgeProps) {
  return (
    <span className="inline-flex h-5 max-w-full items-center rounded-full border border-line-medium bg-surface-raised px-xs align-middle text-body-sm whitespace-nowrap text-ink-body">
      <span className="truncate">{children}</span>
    </span>
  );
}
