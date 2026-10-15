import { attentionKey } from "@/lib/unread";

import { AttentionItem } from "./AttentionItem";
import { attentionNavKey } from "./rows";

import type { AttentionItemResponse } from "@/api/types.gen";

/** Props of {@link AttentionList}. */
export interface AttentionListProps {
  /** The unacknowledged items in the order the server serves them. */
  items: readonly AttentionItemResponse[];
  now: number;
  readSet: ReadonlySet<string>;
  /** Navigation key of the selected item, or `undefined`. */
  selectedKey: string | undefined;
  showSelection: boolean;
  pending: ReadonlySet<string>;
  onOpen: (item: AttentionItemResponse) => void;
  onAcknowledge: (item: AttentionItemResponse) => void;
  onAction: (item: AttentionItemResponse) => void;
}

/** Renders the Needs attention section: its items in served order, never reordered by the client. */
export function AttentionList({
  items,
  now,
  readSet,
  selectedKey,
  showSelection,
  pending,
  onOpen,
  onAcknowledge,
  onAction,
}: AttentionListProps) {
  return (
    <section className="flex flex-col gap-sm">
      <div className="flex flex-wrap items-baseline gap-x-sm">
        <h2 className="font-heading text-h7 font-semibold text-ink">Needs attention</h2>
        <span className="text-body-md text-ink-muted">critical first, then oldest</span>
      </div>
      <ul className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-canvas">
        {items.map((item) => {
          const navKey = attentionNavKey(item.id);
          return (
            <AttentionItem
              key={item.id}
              item={item}
              navKey={navKey}
              now={now}
              isUnread={!readSet.has(attentionKey(item.id))}
              isSelected={selectedKey === navKey}
              showSelection={showSelection}
              isPending={pending.has(item.id)}
              onOpen={onOpen}
              onAcknowledge={onAcknowledge}
              onAction={onAction}
            />
          );
        })}
      </ul>
    </section>
  );
}
