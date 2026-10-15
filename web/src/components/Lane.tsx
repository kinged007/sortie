import { Meter } from "@base-ui/react/meter";

import { CircleAlertFilledIcon, XIcon } from "@/components/icons";
import { markWord } from "@/components/StatusMark";
import { cn } from "@/lib/cn";
import { ruleLabel } from "@/lib/labels";

import type {
  LaneMarker,
  LaneStation,
  StagePosition,
  StageProgress,
  StationState,
} from "@/lib/lane";

/** The longest chain the meter draws one segment per stage for; a longer chain draws one bar. */
const SEGMENT_LIMIT = 12;

const MARKER: Record<LaneMarker, string> = {
  running: "size-3.5 bg-track-done",
  current: "size-3.5 bg-track-done",
  waiting: "size-3.5 border-3 border-track-done bg-canvas",
  retry: "size-3.5 border-2 border-ink-muted bg-canvas",
  held: "size-3.5 border-[1.5px] border-dashed border-ink-muted bg-canvas",
  stopped: "size-3.5 border-[1.5px] border-dashed border-ink-muted bg-canvas",
  needs: "size-4 text-warning-fill",
  handed: "size-3.5 bg-track-done",
  succeeded: "size-3.5 bg-track-done",
  failed: "size-3.5 border-2 border-error-fill bg-canvas",
  degraded: "size-3.5 border-2 border-error-fill bg-canvas",
  clear: "size-3.5 bg-track-done",
  ended:
    "size-3.5 border-2 border-track-done bg-[radial-gradient(circle,var(--color-track-done)_0_3px,var(--color-canvas)_3px)]",
};

const DOT: Record<StationState, string> = {
  passed: "border-track-done bg-track-done",
  current: "border-track-done bg-track-done",
  escalated: "border-track-done bg-canvas",
  waiting: "border-dashed border-track-done bg-canvas",
  ahead: "border-track-todo bg-canvas",
  idle: "border-track-todo bg-canvas",
};

/** Gives the share of the track width, in percent, where the station at `index` of `count` stands. */
export function stationX(index: number, count: number): number {
  return 5 + (index * 90) / Math.max(1, count - 1);
}

function laneMarkerWord(mark: LaneMarker): string {
  return mark === "ended" ? "Ended today" : markWord(mark);
}

/** Props of {@link StageMeter}. */
export interface StageMeterProps {
  position: StagePosition;
  progress: StageProgress;
  /** Draws the current stage in the attention color. */
  needs: boolean;
  /** Whether the issue stands at the agent station; elsewhere the meter shows only the stages done. */
  atAgent: boolean;
  /** Whether the caption shows under the meter; the meter keeps its accessible name without it. */
  caption: boolean;
}

/**
 * Draws the stages of a chain as segments, or as one bar past twelve stages.
 * At the agent station the caption names the stage and its place; elsewhere it
 * counts the stages done.
 */
export function StageMeter({ position, progress, needs, atAgent, caption }: StageMeterProps) {
  const { k, n, done } = progress;
  const focus = atAgent ? progress.focus : null;
  const place = atAgent ? `${k} of ${n}` : `${done} of ${n}`;
  const name = atAgent ? `${ruleLabel(position.current)}, ${place}` : place;
  const stages = Array.from({ length: n }, (_, index) => index + 1);
  const currentTone = needs ? "bg-warning-fill" : "bg-ink";

  return (
    <div className="flex flex-col items-center gap-xxs">
      <Meter.Root value={done} max={n} aria-label={name} className="w-full">
        {n <= SEGMENT_LIMIT ? (
          <div aria-hidden="true" className="flex h-1 gap-px">
            {stages.map((stage) => (
              <span
                key={stage}
                className={cn(
                  "min-w-0 flex-1 rounded-[1px]",
                  stage <= done
                    ? "bg-track-done"
                    : focus?.kind === "current" && stage === k
                      ? currentTone
                      : "bg-track-todo",
                )}
              />
            ))}
          </div>
        ) : (
          <Meter.Track className="relative block h-1 overflow-hidden rounded-full bg-track-todo">
            <Meter.Indicator className="block h-full bg-track-done" />
            {focus?.kind === "current" ? (
              <span
                aria-hidden="true"
                className={cn("absolute inset-y-0", currentTone)}
                style={{
                  left: `${((k - 1) / n) * 100}%`,
                  width: `${100 / n}%`,
                }}
              />
            ) : undefined}
          </Meter.Track>
        )}
      </Meter.Root>
      {caption ? (
        <p aria-hidden="true" className="flex gap-xs whitespace-nowrap text-body-sm text-ink-body">
          {atAgent ? <span className="font-mono">{ruleLabel(position.current)}</span> : undefined}
          <span className="tabular-nums">{place}</span>
        </p>
      ) : undefined}
    </div>
  );
}

/** How much of the lane a {@link Lane} draws: the issue page card, the peek track, a Pipeline lane, or a compressed Pipeline lane. */
export type LaneVariant = "page" | "peek" | "lane" | "dense";

/** Props of {@link Lane}. */
export interface LaneProps {
  stations: LaneStation[];
  position: StagePosition | null;
  progress: StageProgress | null;
  /** Whether a state row decides where the issue stands; a finished issue shows when it ended. */
  live: boolean;
  /** Defaults to `page`, the bordered card with the station names; the other variants draw the bare track. */
  variant?: LaneVariant;
  /** The pull request number the `lane` variant prints under the handoff station once the issue stands there or past it. */
  pullRequestNumber?: number | null;
}

const TRACK_HEIGHT: Record<LaneVariant, string> = {
  page: "h-18",
  peek: "h-10",
  lane: "h-11",
  dense: "h-7",
};

const METER_MARGIN: Record<LaneVariant, string | undefined> = {
  page: "mb-xl",
  peek: "mb-md",
  lane: "mb-lg",
  dense: undefined,
};

/**
 * Draws the stations an issue can stand at as one track, the current station
 * marked with its time above it. The stage meter sits under the agent station,
 * which holds all agent work whatever stage the issue is on. Positions are
 * shares of the width, so the track never scrolls sideways.
 */
export function Lane({
  stations,
  position,
  progress,
  live,
  variant = "page",
  pullRequestNumber = null,
}: LaneProps) {
  const count = stations.length;
  const current = stations.findIndex((station) => station.state === "current");
  const standing = current < 0 ? undefined : stations[current];
  const agent = stations.findIndex((station) => station.kind === "agent");
  const handoff = stations.findIndex((station) => station.kind === "handoff");
  const hasMeter = position !== null && progress !== null && agent >= 0;
  const hasPullRequest =
    variant === "lane" && pullRequestNumber !== null && handoff >= 0 && current >= handoff;
  const reached = current < 0 ? 0 : stationX(current, count) - stationX(0, count);
  const headWord =
    standing === undefined
      ? undefined
      : !live
        ? `ended ${standing.time ?? ""}`.trim()
        : standing.mark === null
          ? undefined
          : laneMarkerWord(standing.mark);

  const track = (
    <div
      aria-hidden="true"
      className={cn("relative", TRACK_HEIGHT[variant], hasMeter && METER_MARGIN[variant])}
    >
      <div
        className="absolute top-1/2 h-px bg-track-todo"
        style={{
          left: `${stationX(0, count)}%`,
          right: `${100 - stationX(count - 1, count)}%`,
        }}
      />
      <div
        className="absolute top-[calc(50%-1px)] h-[3px] rounded-full bg-track-done"
        style={{ left: `${stationX(0, count)}%`, width: `${reached}%` }}
      />
      {stations.map((station, index) => {
        const left = `${stationX(index, count)}%`;
        const isCurrent = index === current;
        return (
          <div key={station.key}>
            {isCurrent ? (
              <CurrentMarker
                station={station}
                left={left}
                variant={variant}
                edge={index === count - 1 ? "end" : index === 0 ? "start" : null}
              />
            ) : (
              <StationDot station={station} left={left} />
            )}
            {variant === "page" ? (
              <span
                className={cn(
                  "absolute top-[calc(50%+12px)] -translate-x-1/2 whitespace-nowrap text-body-sm",
                  isCurrent ? "font-semibold text-ink" : "text-ink-body max-md:hidden",
                )}
                style={{ left }}
              >
                {station.label}
              </span>
            ) : undefined}
          </div>
        );
      })}
      {hasPullRequest ? (
        <span
          className="absolute top-[calc(50%+8px)] -translate-x-1/2 font-mono text-body-sm whitespace-nowrap text-ink-muted max-md:hidden"
          style={{ left: `${stationX(handoff, count)}%` }}
        >
          {`#${pullRequestNumber}`}
        </span>
      ) : undefined}
      {hasMeter ? (
        <div
          className={cn(
            "absolute -translate-x-1/2",
            variant === "page" ? "top-[calc(50%+32px)] w-24" : "top-[calc(50%+16px)] w-14",
          )}
          style={{ left: `${stationX(agent, count)}%` }}
        >
          <StageMeter
            position={position}
            progress={progress}
            needs={stations[agent]?.mark === "needs"}
            atAgent={current === agent}
            caption={variant !== "dense"}
          />
        </div>
      ) : undefined}
    </div>
  );

  const list = (
    <ol className="sr-only" aria-label="Stations">
      {stations.map((station) => (
        <li key={station.key}>
          {[station.label, station.state === "idle" ? undefined : station.state, station.time]
            .filter((part) => part !== undefined && part !== null)
            .join(", ")}
        </li>
      ))}
      {hasMeter ? (
        <li>{`${ruleLabel(position.current)}, ${progress.k} of ${progress.n}`}</li>
      ) : undefined}
    </ol>
  );

  if (variant !== "page") {
    return (
      <div className="min-w-0">
        {list}
        {track}
      </div>
    );
  }
  return (
    <section className="min-w-0 rounded-xl border border-line bg-canvas px-lg py-md max-md:px-md">
      <h2 className="mb-xxs flex items-baseline gap-sm text-body-sm text-ink-muted">
        <span className="font-semibold text-ink-body">Pipeline</span>
        {headWord === undefined ? undefined : <span>{headWord}</span>}
      </h2>
      {list}
      {track}
    </section>
  );
}

interface StationMarkProps {
  station: LaneStation;
  left: string;
}

interface CurrentMarkerProps extends StationMarkProps {
  variant: LaneVariant;
  /** Which end of the track the station sits at, so its time stays inside the card. */
  edge: "start" | "end" | null;
}

function CurrentMarker({ station, left, variant, edge }: CurrentMarkerProps) {
  const mark = station.mark ?? "current";
  return (
    <>
      <span
        className={cn(
          "absolute top-1/2 z-(--z-raised) grid -translate-1/2 place-items-center rounded-full shadow-[0_0_0_2px_var(--color-canvas)]",
          MARKER[mark],
        )}
        style={{ left }}
        title={`${station.label}: ${laneMarkerWord(mark)}`}
      >
        {mark === "needs" ? <CircleAlertFilledIcon className="size-4" /> : undefined}
      </span>
      {station.time === null || variant === "dense" ? undefined : (
        <span
          className={cn(
            "absolute whitespace-nowrap text-body-sm text-ink-body tabular-nums",
            variant === "lane" ? "bottom-[calc(50%+6px)] max-md:hidden" : "bottom-[calc(50%+10px)]",
            edge === "end" ? "-translate-x-full" : edge === "start" ? "" : "-translate-x-1/2",
          )}
          style={{ left }}
        >
          {station.time}
        </span>
      )}
    </>
  );
}

function StationDot({ station, left }: StationMarkProps) {
  if (station.state === "escalated") {
    return (
      <span
        className="absolute top-1/2 -translate-1/2 text-ink-body"
        style={{ left }}
        title={`${station.label}: escalated`}
      >
        <XIcon className="size-3.5" />
      </span>
    );
  }
  return (
    <span
      className={cn(
        "absolute top-1/2 size-[11px] -translate-1/2 rounded-full border-[1.5px]",
        DOT[station.state],
      )}
      style={{ left }}
      title={station.state === "waiting" ? `${station.label}: waiting` : station.label}
    />
  );
}
