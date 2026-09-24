import type { ReactNode } from "react";
import { cn } from "@/ds/lib/cn";

export function Section({
  id,
  title,
  lede,
  children,
}: {
  id: string;
  title: string;
  lede?: string;
  children: ReactNode;
}) {
  return (
    <section id={id} className="scroll-mt-24">
      <h2 className="text-title text-ink">{title}</h2>
      {lede ? (
        <p className="mt-1.5 max-w-[560px] text-body text-ink-muted">{lede}</p>
      ) : null}
      <div className="mt-6 flex flex-col gap-8">{children}</div>
    </section>
  );
}

/* A labeled demo block: caption row on top, demo canvas below. */
export function Demo({
  label,
  hint,
  children,
  className,
  bare = false,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
  className?: string;
  bare?: boolean;
}) {
  return (
    <div>
      <div className="mb-2.5 flex items-baseline justify-between gap-4">
        <span className="text-label text-ink-secondary">{label}</span>
        {hint ? <span className="text-micro text-ink-faint">{hint}</span> : null}
      </div>
      <div
        className={cn(
          !bare &&
            "rounded-lg border-hairline border-edge bg-surface p-5 shadow-sm",
          className,
        )}
      >
        {children}
      </div>
    </div>
  );
}

/* A horizontal row of demo items with even spacing. */
export function DemoRow({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-wrap items-center gap-3", className)}>
      {children}
    </div>
  );
}

/* Keyboard behavior note shown under interactive demos. */
export function KeyboardHint({ children }: { children: ReactNode }) {
  return (
    <p className="mt-3 text-micro text-ink-faint">Keyboard: {children}</p>
  );
}
