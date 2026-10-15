import { Popover as BasePopover } from "@base-ui/react/popover";

import { cn } from "@/lib/cn";

import type { ReactNode } from "react";

/** Props of {@link Popover}. */
export type PopoverProps = BasePopover.Root.Props;

/** Holds the open state of a popover; control it with `open` and `onOpenChange`. */
export function Popover(props: PopoverProps) {
  return <BasePopover.Root {...props} />;
}

/** Props of {@link PopoverTrigger}. */
export type PopoverTriggerProps = BasePopover.Trigger.Props;

/** Renders the button that opens the popover. */
export function PopoverTrigger(props: PopoverTriggerProps) {
  return <BasePopover.Trigger {...props} />;
}

/** Props of {@link PopoverPopup}. */
export interface PopoverPopupProps {
  /** Accessible name of the popup. */
  "aria-label"?: string;
  children: ReactNode;
  side?: BasePopover.Positioner.Props["side"];
  align?: BasePopover.Positioner.Props["align"];
  className?: string;
}

/**
 * Renders the floating panel below its trigger by default; it rises 4 px from the
 * trigger's side on entry.
 */
export function PopoverPopup({
  children,
  side = "bottom",
  align = "start",
  className,
  ...rest
}: PopoverPopupProps) {
  return (
    <BasePopover.Portal>
      <BasePopover.Positioner
        side={side}
        align={align}
        sideOffset={4}
        className="z-(--z-dropdown) outline-hidden"
      >
        <BasePopover.Popup
          className={cn(
            "origin-(--transform-origin) rounded-lg border border-line bg-canvas text-ink shadow-lg outline-hidden",
            "transition-[opacity,translate] duration-(--duration-enter-small) ease-enter",
            "data-ending-style:duration-(--duration-exit) data-ending-style:ease-exit",
            "data-starting-style:opacity-0 data-ending-style:opacity-0",
            "data-[side=top]:data-starting-style:translate-y-(--offset-anchored) data-[side=top]:data-ending-style:translate-y-(--offset-anchored)",
            "data-[side=bottom]:data-starting-style:-translate-y-(--offset-anchored) data-[side=bottom]:data-ending-style:-translate-y-(--offset-anchored)",
            className,
          )}
          {...rest}
        >
          {children}
        </BasePopover.Popup>
      </BasePopover.Positioner>
    </BasePopover.Portal>
  );
}
