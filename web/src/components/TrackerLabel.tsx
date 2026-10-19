/** Props of {@link TrackerLabel}. */
export interface TrackerLabelProps {
  children: string;
}

/** Renders a tracker label as a rounded tag, distinct from a state badge. */
export function TrackerLabel({ children }: TrackerLabelProps) {
  return (
    <span className="inline-flex h-5 max-w-full items-center rounded-full border border-line-medium bg-canvas px-xs align-middle text-body-sm whitespace-nowrap text-ink-body">
      <span className="truncate">{children}</span>
    </span>
  );
}
