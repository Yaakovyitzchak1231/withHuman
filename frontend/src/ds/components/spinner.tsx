import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";

export interface SpinnerProps extends ComponentProps<"svg"> {
  size?: number;
}

/* Quiet rotating arc in currentColor; defaults to muted ink. */
export function Spinner({ size = 16, className, ...props }: SpinnerProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      role="status"
      aria-label="Loading"
      className={cn("animate-spin text-ink-muted", className)}
      {...props}
    >
      <circle
        cx="8"
        cy="8"
        r="6.25"
        stroke="currentColor"
        strokeOpacity="0.25"
        strokeWidth="1.5"
      />
      <path
        d="M8 1.75 A6.25 6.25 0 0 1 14.25 8"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      />
    </svg>
  );
}
