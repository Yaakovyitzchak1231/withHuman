import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { InfoIcon } from "@/ds/icons";
import type { Tone } from "./badge";

const TONE_BG: Record<Tone, string> = {
  neutral: "bg-fill-hover",
  accent: "bg-accent-subtle",
  approve: "bg-approve-subtle",
  deny: "bg-deny-subtle",
  warn: "bg-warn-subtle",
};

const TONE_ICON: Record<Tone, string> = {
  neutral: "text-ink-muted",
  accent: "text-accent",
  approve: "text-approve",
  deny: "text-deny",
  warn: "text-warn",
};

export interface CalloutProps extends ComponentProps<"div"> {
  tone?: Tone;
  /* Leading 16px icon; defaults to the info glyph. */
  icon?: ReactNode;
  title?: string;
}

/* Inline notice: the static sibling of Toast. The icon carries the
 * tone; the text stays ink. */
export function Callout({
  tone = "neutral",
  icon,
  title,
  className,
  children,
  ...props
}: CalloutProps) {
  return (
    <div
      className={cn(
        "flex gap-2.5 rounded-md px-3 py-2.5",
        TONE_BG[tone],
        className,
      )}
      {...props}
    >
      <span className={cn("mt-0.5 flex shrink-0", TONE_ICON[tone])}>
        {icon ?? <InfoIcon width={15} height={15} />}
      </span>
      <div className="flex min-w-0 flex-col gap-0.5 text-body">
        {title ? <p className="font-medium text-ink">{title}</p> : null}
        <div className="text-ink-secondary">{children}</div>
      </div>
    </div>
  );
}
