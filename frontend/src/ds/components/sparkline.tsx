import { useId } from "react";
import { cn } from "@/ds/lib/cn";

/*
 * A trend in a cell: one series as a thin line, the newest point marked
 * in the accent. It carries no axes, so it stands beside a figure that
 * gives the scale rather than replacing it. The baseline is always
 * zero, so two sparklines in one column compare shape honestly, and a
 * series that is all zeros draws as a flat faint line rather than
 * nothing.
 */
export interface SparklineProps {
  /* Oldest first. */
  values: number[];
  width?: number;
  height?: number;
  /* Names the series for assistive tech; the figure beside it gives
   * the value. */
  label: string;
  className?: string;
}

const STROKE = 1.5;
const DOT = 2;

export function Sparkline({ values, width = 56, height = 18, label, className }: SparklineProps) {
  const id = useId();
  const max = Math.max(0, ...values);
  const inset = DOT + STROKE / 2;
  const step = values.length > 1 ? (width - inset * 2) / (values.length - 1) : 0;
  const y = (value: number) =>
    max === 0 ? height - inset : height - inset - (value / max) * (height - inset * 2);
  const points = values.map((value, index) => [inset + index * step, y(value)] as const);
  const path = points.map(([px, py], index) => `${index === 0 ? "M" : "L"}${px} ${py}`).join(" ");
  const last = points[points.length - 1];
  const flat = max === 0;
  return (
    <svg
      role="img"
      aria-labelledby={id}
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      className={cn("shrink-0 overflow-visible", flat ? "text-ink-faint" : "text-ink-muted", className)}
    >
      <title id={id}>{label}</title>
      <path
        d={path}
        fill="none"
        stroke="currentColor"
        strokeWidth={STROKE}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      {last && !flat ? <circle cx={last[0]} cy={last[1]} r={DOT} className="fill-accent" /> : null}
    </svg>
  );
}
