import { keyTip } from "@/app/key-tips";
import { ExternalLinkIcon } from "@/components/icons";
import { ServedText } from "@/components/ServedText";
import { StatusMark } from "@/components/StatusMark";
import { Button, buttonStyles } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { fmtAgo } from "@/lib/format";

import { rowStyles, secondLineColor } from "./rows";
import { linkActionOf } from "./use-attention-actions";

import type { MouseEvent } from "react";

import type { AttentionItemResponse } from "@/api/types.gen";

/** Props of {@link AttentionItem}. */
export interface AttentionItemProps {
  item: AttentionItemResponse;
  /** Key of the item in the keyboard selection. */
  navKey: string;
  /** Server-clock time in epoch milliseconds that the item's age measures against. */
  now: number;
  isUnread: boolean;
  /** Whether this is the selected item; its action takes the primary style. */
  isSelected: boolean;
  /** Whether the selected-row look shows, which it does from the first navigation key on. */
  showSelection: boolean;
  /** Whether the item's acknowledge request is running. */
  isPending: boolean;
  onOpen: (item: AttentionItemResponse) => void;
  onAcknowledge: (item: AttentionItemResponse) => void;
  /** Called when the reader follows the link of the item's action. */
  onAction: (item: AttentionItemResponse) => void;
}

/**
 * Renders one attention item: why it exists, what clears it, and its actions.
 * A click outside its buttons opens the peek of its issue.
 */
export function AttentionItem({
  item,
  navKey,
  now,
  isUnread,
  isSelected,
  showSelection,
  isPending,
  onOpen,
  onAcknowledge,
  onAction,
}: AttentionItemProps) {
  const link = linkActionOf(item);
  const key = item.display_identifier || item.issue_identifier;
  const isCritical = item.severity === "critical";
  const isOpenable = Boolean(item.issue_identifier);
  const isLook = isSelected && showSelection;

  function handleClick(event: MouseEvent<HTMLLIElement>) {
    if (!isOpenable) {
      return;
    }
    if (event.target instanceof Element && event.target.closest("a, button") !== null) {
      return;
    }
    onOpen(item);
  }

  function handleLinkClick(event: MouseEvent<HTMLAnchorElement>) {
    if (isPending) {
      event.preventDefault();
      return;
    }
    onAction(item);
  }

  return (
    <li
      data-nav-key={navKey}
      onClick={handleClick}
      className={cn("flex gap-sm px-md py-md", rowStyles(isLook, isOpenable))}
    >
      <span className="flex h-5 shrink-0 items-center">
        <span aria-hidden="true" className={cn("size-xs rounded-full", isUnread && "bg-primary")} />
      </span>
      <StatusMark mark={isCritical ? "degraded" : "needs"} className="h-5 shrink-0" />
      <div className="flex min-w-0 flex-1 flex-col gap-xxs [overflow-wrap:anywhere]">
        <p className="text-body-md text-ink">
          <strong className={cn("font-bold", isCritical ? "text-error" : "text-warning")}>
            <ServedText text={item.reason_text} />
          </strong>
          {item.issue_title ? <> {item.issue_title}</> : undefined}
          {key ? <span className="tabular-nums text-ink-body"> {key}</span> : undefined}
        </p>
        <p className="text-body-md text-ink-body">
          <ServedText text={item.why_text} />
        </p>
        <p className="text-body-md text-ink-body">
          <ServedText text={item.clears_text} />
        </p>
        {item.since === null ? undefined : (
          <p className={cn("text-body-sm tabular-nums", secondLineColor(isLook))}>
            {fmtAgo(item.since, now)}
          </p>
        )}
        <div className="flex flex-wrap gap-xs pt-xs">
          {link === null ? undefined : (
            <a
              href={link.url}
              target="_blank"
              rel="noopener noreferrer"
              title={keyTip(link.title, "A")}
              aria-disabled={isPending}
              data-disabled={isPending ? "" : undefined}
              onClick={handleLinkClick}
              className={buttonStyles(isSelected ? "primary" : "secondary")}
            >
              {link.label}
              <ExternalLinkIcon />
            </a>
          )}
          <Button
            variant={isSelected && link === null ? "primary" : "secondary"}
            disabled={isPending}
            title={keyTip("Acknowledge", "E")}
            onClick={() => onAcknowledge(item)}
          >
            {isPending ? "Acknowledging" : "Acknowledge"}
          </Button>
        </div>
      </div>
    </li>
  );
}
