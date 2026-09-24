import { useEffect, useRef, type ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import { LinkIcon } from "@/ds/icons";

export interface SettingsSectionProps {
  /* Omitted for a page that is one list and nothing else (a picker):
   * the card stands alone, with no heading and no anchor. */
  title?: string;
  /* The anchor the title answers to in the URL hash; defaults to the
   * title slugified ("Verified domains" is #verified-domains). Pass one
   * when two sections on a page share a title. */
  id?: string;
  description?: ReactNode;
  /* A small control on the title line: Add channel, New rule. */
  action?: ReactNode;
  /* danger marks the section that holds irreversible actions. */
  tone?: "default" | "danger";
  /* The card's inset. "rows" is for SettingRows, which carry their own
   * vertical padding. "none" is for content that fills the card edge to
   * edge: a Table, an EmptyState, a list with its own inset. */
  inset?: "rows" | "none";
  /* Controls that narrow what the card shows (a search, a filter),
   * between the title line and the card. */
  toolbar?: ReactNode;
  /* A quiet line under the card: what the section cannot offer here,
   * or how what it holds is applied. */
  note?: ReactNode;
  children: ReactNode;
  className?: string;
}

/* "1. Single sign-on" is #single-sign-on: the numbering is presentation,
 * the anchor is the name. */
export function anchorFor(title: string) {
  return title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/* A titled group of settings on one surface card: the only way a
 * settings page groups anything. Sections stack in a SettingsPage. Every
 * title is an anchor, so a section can be linked to and lands in view
 * when the page opens on its hash. */
export function SettingsSection({
  title,
  id,
  description,
  action,
  tone = "default",
  inset = "rows",
  toolbar,
  note,
  children,
  className,
}: SettingsSectionProps) {
  const anchor = id ?? (title ? anchorFor(title) : undefined);
  const ref = useRef<HTMLElement>(null);

  /* The page's content arrives with the route loader, after the browser
   * has already tried to honour the hash, so the section scrolls itself
   * into view once it exists. */
  useEffect(() => {
    if (anchor && window.location.hash === `#${anchor}`) {
      ref.current?.scrollIntoView({ block: "start" });
    }
  }, [anchor]);

  return (
    <section
      ref={ref}
      id={anchor}
      className={cn("flex scroll-mt-8 flex-col gap-3", className)}
    >
      {title || action ? (
        <div className="flex items-start justify-between gap-4">
          <div className="flex min-w-0 flex-col gap-1">
            {title ? (
              <h2
                className={cn(
                  "group/anchor text-body font-medium",
                  tone === "danger" ? "text-deny" : "text-ink",
                )}
              >
                <a
                  href={`#${anchor}`}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-xs",
                    focusRing,
                  )}
                >
                  {title}
                  <LinkIcon
                    width={13}
                    height={13}
                    className={cn(
                      "text-ink-faint opacity-0 transition-opacity duration-100",
                      "group-hover/anchor:opacity-100 group-focus-within/anchor:opacity-100",
                    )}
                  />
                </a>
              </h2>
            ) : null}
            {description ? (
              <p className="max-w-[560px] text-label font-normal text-ink-muted">
                {description}
              </p>
            ) : null}
          </div>
          {action ? (
            <div className="flex shrink-0 items-center">{action}</div>
          ) : null}
        </div>
      ) : null}
      {toolbar ? <div className="flex items-center gap-3">{toolbar}</div> : null}
      <div
        className={cn(
          "rounded-lg border-hairline bg-surface shadow-sm",
          inset === "rows" ? "px-4" : "overflow-hidden",
          tone === "danger" ? "border-deny/40" : "border-edge",
        )}
      >
        {children}
      </div>
      {note ? (
        <p className="max-w-[560px] text-label font-normal text-ink-muted">
          {note}
        </p>
      ) : null}
    </section>
  );
}
