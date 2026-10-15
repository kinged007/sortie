import { useStateQuery } from "@/api/queries";
import { useLive, useNow } from "@/app/live";
import { cn } from "@/lib/cn";
import { fmtClockSec, fmtDue, parseInstant } from "@/lib/format";

import type { StateResponse } from "@/api/types.gen";

type Tone = "live" | "idle" | "error";

interface Indicator {
  text: string;
  tone: Tone;
}

const TONE_DOT: Record<Tone, string> = {
  live: "bg-success-fill",
  idle: "bg-ink-muted",
  error: "bg-error-fill",
};

function nextPoll(state: StateResponse | undefined, now: number): string | null {
  const due = parseInstant(state?.health?.poll.next_poll_at);
  return due === null ? null : fmtDue((due - now) / 1000);
}

interface IndicatorInput {
  paused: boolean;
  isHolding: boolean;
  isStreamOpen: boolean;
  state: StateResponse | undefined;
  lastUpdateAt: number | null;
  hasFailed: boolean;
  now: number;
}

function indicatorFor(input: IndicatorInput): Indicator {
  const { state, now } = input;
  if (input.paused) {
    return { text: "Paused · press P to resume", tone: "idle" };
  }
  if (input.isHolding) {
    return { text: "Holding still while you read", tone: "idle" };
  }
  if (!input.isStreamOpen) {
    return { text: "Reconnecting", tone: "idle" };
  }
  if (input.hasFailed) {
    const since =
      input.lastUpdateAt === null ? "" : ` · last update ${fmtClockSec(input.lastUpdateAt)}`;
    return { text: `Not updating${since}`, tone: "error" };
  }
  const next = nextPoll(state, now);
  if (state?.health?.poll.status === "fail") {
    return { text: `Poll failing · next try ${next ?? "unknown"}`, tone: "error" };
  }
  return { text: next === null ? "Live" : `Live · next poll ${next}`, tone: "live" };
}

/** Props of {@link LiveIndicator}. */
export interface LiveIndicatorProps {
  /** True while the peek holds Overview still. */
  isHolding: boolean;
  onTogglePause: () => void;
}

/** Renders the button that says how fresh the screen is and pauses or resumes live updates. */
export function LiveIndicator({ isHolding, onTogglePause }: LiveIndicatorProps) {
  const { paused, streamStatus } = useLive();
  const { data, isError, dataUpdatedAt } = useStateQuery();
  const now = useNow();
  const { text, tone } = indicatorFor({
    paused,
    isHolding,
    isStreamOpen: streamStatus === "open",
    state: data,
    lastUpdateAt: dataUpdatedAt === 0 ? null : dataUpdatedAt,
    hasFailed: isError,
    now,
  });

  return (
    <button
      type="button"
      onClick={onTogglePause}
      title="Pause or resume live updates (P)"
      className="inline-flex h-(--size-control) cursor-pointer items-center gap-xs rounded-lg px-sm text-ink-body transition-colors duration-(--duration-feedback) hover:bg-state-hover hover:text-ink focus-visible:-outline-offset-1 focus-visible:outline-2 focus-visible:outline-focus-ring active:bg-state-pressed"
    >
      <span aria-hidden="true" className={cn("size-xs shrink-0 rounded-full", TONE_DOT[tone])} />
      {text}
    </button>
  );
}
