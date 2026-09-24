import { Sparkline } from "@/ds/components/sparkline";
import { Tooltip } from "@/ds/components/tooltip";
import { ClockIcon } from "@/ds/icons";
import { cn } from "@/ds/lib/cn";
import { plural } from "./format";

/* Seven days of requests as a trend beside the total, in a table cell.
 * The tooltip spells the total out and, for a row that knows it, adds
 * how many still wait for a decision; a non-zero pending count also
 * marks the cell with a clock. */
export function RequestTrend({
  label,
  total,
  trend,
  pending = 0,
}: {
  /* Names the series for assistive tech: the agent or instance. */
  label: string;
  total: number;
  /* Oldest first. */
  trend: number[];
  pending?: number;
}) {
  const summary =
    `${plural(total, "request")} in the last 7 days` +
    (pending > 0 ? `, ${pending} pending` : "");
  return (
    <Tooltip content={summary}>
      <span className="inline-flex items-center gap-2">
        <Sparkline values={trend} label={`${label}: requests per day`} />
        <span className={cn("text-body tabular-nums", total === 0 ? "text-ink-faint" : "text-ink")}>
          {total}
        </span>
        {pending > 0 ? (
          <ClockIcon width={12} height={12} className="shrink-0 text-warn" />
        ) : null}
      </span>
    </Tooltip>
  );
}
