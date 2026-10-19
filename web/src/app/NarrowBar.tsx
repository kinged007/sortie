import { useStateQuery } from "@/api/queries";
import { BrandMark } from "@/app/BrandMark";
import { keyTip } from "@/app/key-tips";
import { modifierLabel } from "@/app/keys";
import { SearchIcon } from "@/components/icons";
import { StatusMark } from "@/components/StatusMark";
import { Button } from "@/components/ui/button";
import { condition } from "@/lib/condition";
import { pluralVerb } from "@/lib/format";

import type { StateResponse } from "@/api/types.gen";
import type { ReactNode } from "react";

function statusLabel(state: StateResponse): ReactNode {
  const { subject, verb } = pluralVerb(
    state.counts.attention,
    ["item", "items"],
    ["needs", "need"],
  );
  const needs = `${subject} ${verb} attention`;
  switch (condition(state)) {
    case "degraded":
      return state.counts.attention > 0 ? (
        <span className="flex flex-wrap gap-x-xs">
          <span>Degraded</span> <span>{needs}</span>
        </span>
      ) : (
        "Degraded"
      );
    case "needs":
      return needs;
    case "clear":
      return "All clear";
  }
}

/** Props of {@link NarrowBar}. */
export interface NarrowBarProps {
  onOpenPalette: () => void;
}

/** Renders the sticky bar that replaces the rail below 768 px: brand, condition, and the Menu button. */
export function NarrowBar({ onOpenPalette }: NarrowBarProps) {
  const { data } = useStateQuery();

  return (
    <header className="sticky top-0 z-(--z-sticky) flex items-center gap-sm border-b border-line bg-canvas px-md py-sm md:hidden">
      <BrandMark />
      <span className="font-heading font-semibold">Sortie</span>
      {data === undefined ? undefined : (
        <span className="min-w-0 text-body-sm wrap-anywhere">
          <StatusMark mark={condition(data)} label={statusLabel(data)} />
        </span>
      )}
      <Button
        className="ml-auto"
        onClick={onOpenPalette}
        title={keyTip("Open commands", modifierLabel(), "K")}
      >
        <SearchIcon />
        Menu
      </Button>
    </header>
  );
}
