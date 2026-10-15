import { Autocomplete as BaseAutocomplete } from "@base-ui/react/autocomplete";

import { SearchIcon } from "@/components/icons";
import { cn } from "@/lib/cn";

import type { ReactNode } from "react";

/** Props of {@link Autocomplete}. */
export type AutocompleteProps<ItemValue> = Omit<
  BaseAutocomplete.Root.Props<ItemValue>,
  "open" | "inline" | "autoHighlight" | "keepHighlight" | "items"
> & {
  /** A flat list; the palette has no groups. */
  items: readonly ItemValue[];
};

/**
 * Holds the query and the filtered items of a list that is always open and renders
 * in place, as a command palette does inside a dialog. The first match stays
 * highlighted, so `Enter` runs it.
 */
export function Autocomplete<ItemValue>(props: AutocompleteProps<ItemValue>) {
  return <BaseAutocomplete.Root open inline autoHighlight="always" keepHighlight {...props} />;
}

/** Props of {@link AutocompleteInput}. */
export type AutocompleteInputProps = Omit<BaseAutocomplete.Input.Props, "className">;

/** Renders the search field with its icon and a bottom hairline. */
export function AutocompleteInput(props: AutocompleteInputProps) {
  return (
    <BaseAutocomplete.InputGroup className="flex items-center gap-xs border-b border-line bg-field px-md text-ink-muted focus-within:outline-2 focus-within:-outline-offset-2 focus-within:outline-focus-ring">
      <SearchIcon />
      <BaseAutocomplete.Input
        className="h-10 w-full min-w-0 bg-transparent text-body-md text-ink outline-none placeholder:text-ink-muted"
        {...props}
      />
    </BaseAutocomplete.InputGroup>
  );
}

/** Props of {@link AutocompleteList}. */
export type AutocompleteListProps = Omit<BaseAutocomplete.List.Props, "className">;

/** Renders the scrolling list of items. */
export function AutocompleteList(props: AutocompleteListProps) {
  return (
    <BaseAutocomplete.List
      className="min-h-0 overflow-y-auto overscroll-contain py-xxs [scroll-padding-block:4px]"
      {...props}
    />
  );
}

/** Props of {@link AutocompleteItem}. */
export interface AutocompleteItemProps extends Omit<BaseAutocomplete.Item.Props, "className"> {
  className?: string;
}

/** Renders one item; hover and the keyboard highlight share one color. */
export function AutocompleteItem({ className, ...rest }: AutocompleteItemProps) {
  return (
    <BaseAutocomplete.Item
      className={cn(
        "flex min-h-8 cursor-default items-center justify-between gap-md px-md py-xxs text-body-md text-ink outline-none select-none data-highlighted:bg-item-hover active:bg-item-selected",
        className,
      )}
      {...rest}
    />
  );
}

/** Props of {@link AutocompleteEmpty}. */
export interface AutocompleteEmptyProps {
  children: ReactNode;
}

/** Renders its children only while no item matches the query. */
export function AutocompleteEmpty({ children }: AutocompleteEmptyProps) {
  return (
    <BaseAutocomplete.Empty className="px-md py-lg text-body-md text-ink-muted empty:hidden">
      {children}
    </BaseAutocomplete.Empty>
  );
}
