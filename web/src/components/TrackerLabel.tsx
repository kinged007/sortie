/** Props of {@link TrackerLabel}. */
export interface TrackerLabelProps {
  children: string;
}

/** Renders a tracker label as a rounded tag with a leading dot, distinct from a state badge. */
export function TrackerLabel({ children }: TrackerLabelProps) {
  return (
    <span className="inline-flex h-5 max-w-full items-center gap-xxs rounded-full border border-line-medium bg-canvas pr-xs pl-[7px] align-middle text-body-sm whitespace-nowrap text-ink-body">
      <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-ink-muted" />
      <span className="truncate">{children}</span>
    </span>
  );
}
