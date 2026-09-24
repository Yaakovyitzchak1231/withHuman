import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";

export function LoadingDots({ className, ...props }: ComponentProps<"span">) {
  return (
    <span
      role="status"
      aria-label="Loading"
      className={cn("inline-flex h-4 shrink-0 items-center gap-1 text-ink-muted", className)}
      {...props}
    >
      {[0, 1, 2].map((dot) => (
        <span
          key={dot}
          aria-hidden
          className="size-1 rounded-full bg-current animate-[ds-dot_1.2s_ease-in-out_infinite] motion-reduce:animate-none"
          style={{ animationDelay: `${dot * 160}ms` }}
        />
      ))}
    </span>
  );
}
