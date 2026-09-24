import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { ToneIcon, type Tone } from "./badge";

/* Only the marker carries the tone; content stays ink. */
const MARKER_TONES: Record<Tone, string> = {
  neutral: "text-ink-muted",
  accent: "text-accent",
  approve: "text-approve",
  deny: "text-deny",
  warn: "text-warn",
};

/*
 * Vertical rail of ordered moments: escalation path steps, audit
 * evidence, notification history. The connector is a hairline between
 * markers; the last item ends the rail. Timelines nest: an item may hold
 * a whole timeline of its own (a pipeline branch's blocks), and each
 * rail ends at its own last item.
 */
export function Timeline({ className, ...props }: ComponentProps<"ol">) {
  return <ol className={cn("flex flex-col", className)} {...props} />;
}

export interface TimelineItemProps extends ComponentProps<"li"> {
  tone?: Tone;
  /* Leading 14-16px icon; defaults to the tone's status icon. */
  marker?: ReactNode;
}

export function TimelineItem({
  tone = "neutral",
  marker,
  className,
  children,
  ...props
}: TimelineItemProps) {
  return (
    <li
      className={cn(
        "relative flex items-baseline gap-3 pb-5 last:pb-0",
        /* The connector is the item's own pseudo-element, so only this
         * item being last hides it: an enclosing timeline's last item
         * never reaches into a nested one. */
        "after:absolute after:top-[22px] after:bottom-0 after:left-[8.5px] after:w-px after:bg-edge last:after:hidden",
        className,
      )}
      {...props}
    >
      <span
        className={cn(
          "w-[18px] shrink-0 text-body",
          MARKER_TONES[tone],
        )}
      >
        {/* Share the content's first baseline, even when a chip makes
         * that line taller. Middle aligns the icon with the text's
         * x-height instead of pinning it to the top of the item. */}
        {/* Rail node, not a status affordance: status in content is
         * always a labeled Badge. The wrapper carries the tone. */}
        <span className="inline-flex size-[18px] items-center justify-center align-middle">
          {marker ?? <ToneIcon tone={tone} />}
        </span>
      </span>
      <div className="min-w-0 flex-1">{children}</div>
    </li>
  );
}
