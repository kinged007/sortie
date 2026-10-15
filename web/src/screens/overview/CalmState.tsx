import { fmtClock, fmtCount } from "@/lib/format";

/** Props of {@link CalmState}. */
export interface CalmStateProps {
  /** `counts.running` of the state. */
  running: number;
  /** Size of today's ended set. */
  endedCount: number;
  /** `observed_at` of the newest ending today, or `null` while the set is empty. */
  lastEndedAt: string | null;
  /** Runs finished today, or `undefined` while the tally is unavailable. */
  runsFinished: number | undefined;
}

interface TileProps {
  label: string;
  value: string;
}

function Tile({ label, value }: TileProps) {
  return (
    <div className="rounded-xl border border-line bg-surface p-md text-body-sm text-ink-muted">
      <dt>{label}</dt>
      <dd className="mt-xs font-heading text-h5 font-semibold text-ink tabular-nums">{value}</dd>
    </div>
  );
}

/** Renders the calm line and the stat tiles that follow the headline while nothing needs attention. */
export function CalmState({ running, endedCount, lastEndedAt, runsFinished }: CalmStateProps) {
  return (
    <section aria-label="Calm" className="mb-md">
      <p className="text-body-md text-ink-body">
        Nothing is parked, held, escalated or stopped, and Sortie is healthy.
      </p>
      <dl className="mt-xl grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] gap-md">
        <Tile label="Agents running" value={fmtCount(running)} />
        <Tile label="Ended today" value={fmtCount(endedCount)} />
        {lastEndedAt === null ? undefined : (
          <Tile label="Last one ended" value={fmtClock(lastEndedAt)} />
        )}
        {runsFinished === undefined ? undefined : (
          <Tile label="Runs finished today" value={fmtCount(runsFinished)} />
        )}
      </dl>
    </section>
  );
}
