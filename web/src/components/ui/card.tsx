import type { ReactNode } from "react";

/** Props of {@link Card}. */
export interface CardProps {
  /** The heading over the content, drawn as a title bar; leave it out when the content sits under a heading that covers more than the content. */
  title?: ReactNode;
  children: ReactNode;
}

/** Renders the rounded frame of a dashboard block with an optional title bar over its content. */
export function Card({ title, children }: CardProps) {
  return (
    <div className="max-w-full overflow-hidden rounded-xl border border-line bg-canvas">
      {title === undefined ? undefined : (
        <h2 className="border-b border-line px-md py-sm text-body-md font-medium text-ink">
          {title}
        </h2>
      )}
      {children}
    </div>
  );
}
