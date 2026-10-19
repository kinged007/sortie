import { Card } from "@/components/ui/card";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";
import { fmtCount, fmtPercent } from "@/lib/format";

import type { Summary } from "@/api/types.gen";

interface Segment {
  label: string;
  count: number;
  fill: string;
}

function segmentsOf(summary: Summary): Segment[] {
  return [
    { label: "Succeeded", count: summary.succeeded, fill: "bg-outcome-ok" },
    { label: "Stopped for a person or budget", count: summary.stopped, fill: "bg-outcome-stopped" },
    { label: "Cancelled", count: summary.cancelled, fill: "bg-outcome-cancelled" },
    { label: "Failed", count: summary.failed, fill: "bg-outcome-failed" },
  ].filter((segment) => segment.count > 0);
}

function roundingOf(index: number, count: number): string | undefined {
  if (count === 1) {
    return "rounded";
  }
  if (index === 0) {
    return "rounded-l";
  }
  return index === count - 1 ? "rounded-r" : undefined;
}

/** Props of {@link OutcomeBar}. */
export interface OutcomeBarProps {
  summary: Summary;
}

/**
 * Renders how the runs of a range ended as a legend and a stacked bar, one segment per outcome
 * that has runs, each with a tooltip of its count and share of the runs. Renders nothing while no
 * outcome has runs.
 */
export function OutcomeBar({ summary }: OutcomeBarProps) {
  const segments = segmentsOf(summary);
  if (segments.length === 0) {
    return undefined;
  }

  return (
    <Card title="Outcomes">
      <div className="flex flex-col gap-xs px-md py-sm">
        <div className="flex flex-wrap gap-x-md gap-y-xxs text-body-sm text-ink-body">
          {segments.map((segment) => (
            <span key={segment.label} className="inline-flex items-center gap-xs">
              <span aria-hidden="true" className={cn("size-sm rounded", segment.fill)} />
              {segment.label} {fmtCount(segment.count)}
            </span>
          ))}
        </div>
        <div aria-hidden="true" className="flex h-4 gap-xxs">
          {segments.map((segment, index) => (
            <Tooltip key={segment.label}>
              <TooltipTrigger
                delay={0}
                render={
                  <div
                    className={cn(
                      "min-w-[4px] basis-0",
                      segment.fill,
                      roundingOf(index, segments.length),
                    )}
                    style={{ flexGrow: segment.count }}
                  />
                }
              />
              <TooltipPopup>
                <div className="min-w-35">
                  <div className="mb-xxs font-bold">{segment.label}</div>
                  <div className="flex items-center gap-xs">
                    Runs
                    <b className="ml-auto font-medium tabular-nums">{fmtCount(segment.count)}</b>
                  </div>
                  <div className="flex items-center gap-xs">
                    Share
                    <b className="ml-auto font-medium tabular-nums">
                      {fmtPercent(segment.count / summary.runs)}
                    </b>
                  </div>
                </div>
              </TooltipPopup>
            </Tooltip>
          ))}
        </div>
      </div>
    </Card>
  );
}
