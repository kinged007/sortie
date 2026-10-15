import { Menu as BaseMenu } from "@base-ui/react/menu";

import { CheckIcon } from "@/components/icons";
import { cn } from "@/lib/cn";

import type { ReactNode } from "react";

/** Props of {@link Menu}. */
export type MenuProps = BaseMenu.Root.Props;

/** Holds the open state of a menu; compose it with the parts below. */
export function Menu(props: MenuProps) {
  return <BaseMenu.Root {...props} />;
}

/** Props of {@link MenuTrigger}. */
export type MenuTriggerProps = BaseMenu.Trigger.Props;

/** Renders the button that opens the menu. */
export function MenuTrigger(props: MenuTriggerProps) {
  return <BaseMenu.Trigger {...props} />;
}

/** Props of {@link MenuPopup}. */
export interface MenuPopupProps {
  children: ReactNode;
  /** The side of the trigger the popup opens toward; it rises 4 px from that side. */
  side?: BaseMenu.Positioner.Props["side"];
  align?: BaseMenu.Positioner.Props["align"];
  className?: string;
}

/** Renders the floating list of a menu above the page, outside any dark ancestor such as the rail. */
export function MenuPopup({
  children,
  side = "bottom",
  align = "start",
  className,
}: MenuPopupProps) {
  return (
    <BaseMenu.Portal>
      <BaseMenu.Positioner
        side={side}
        align={align}
        sideOffset={4}
        className="z-(--z-menu) outline-hidden"
      >
        <BaseMenu.Popup
          className={cn(
            "min-w-40 origin-(--transform-origin) rounded-lg border border-line bg-canvas py-xxs text-ink shadow-lg outline-hidden",
            "transition-[opacity,translate] duration-(--duration-enter-small) ease-enter",
            "data-ending-style:duration-(--duration-exit) data-ending-style:ease-exit",
            "data-starting-style:opacity-0 data-ending-style:opacity-0",
            "data-[side=top]:data-starting-style:translate-y-(--offset-anchored) data-[side=top]:data-ending-style:translate-y-(--offset-anchored)",
            "data-[side=bottom]:data-starting-style:-translate-y-(--offset-anchored) data-[side=bottom]:data-ending-style:-translate-y-(--offset-anchored)",
            className,
          )}
        >
          {children}
        </BaseMenu.Popup>
      </BaseMenu.Positioner>
    </BaseMenu.Portal>
  );
}

/** Props of {@link MenuRadioGroup}. */
export interface MenuRadioGroupProps<Value extends string> {
  value: Value;
  onValueChange: (value: Value) => void;
  children: ReactNode;
}

/** Groups radio items so that exactly one is checked. */
export function MenuRadioGroup<Value extends string>({
  value,
  onValueChange,
  children,
}: MenuRadioGroupProps<Value>) {
  return (
    <BaseMenu.RadioGroup value={value} onValueChange={(next) => onValueChange(next as Value)}>
      {children}
    </BaseMenu.RadioGroup>
  );
}

/** Props of {@link MenuRadioItem}. */
export interface MenuRadioItemProps {
  value: string;
  children: ReactNode;
}

/** Renders a choice of a radio group, with the check beside the chosen one. */
export function MenuRadioItem({ value, children }: MenuRadioItemProps) {
  return (
    <BaseMenu.RadioItem
      value={value}
      className="grid cursor-default grid-cols-[var(--size-icon)_1fr] items-center gap-xs px-sm py-xs text-body-md outline-hidden select-none data-highlighted:bg-item-hover active:bg-item-selected"
    >
      <BaseMenu.RadioItemIndicator className="col-start-1 flex text-ink-muted">
        <CheckIcon strokeWidth={2.5} />
      </BaseMenu.RadioItemIndicator>
      <span className="col-start-2 flex items-center gap-xs">{children}</span>
    </BaseMenu.RadioItem>
  );
}
