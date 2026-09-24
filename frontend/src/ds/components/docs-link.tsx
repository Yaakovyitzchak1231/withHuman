import {
  HoverCard,
  HoverCardBody,
  HoverCardTitle,
} from "./hover-card";
import { ArrowUpRightIcon } from "@/ds/icons";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

/** A documentation link showing the article's title and one-sentence summary. */
export function DocsLink({
  href,
  title,
  description,
}: {
  href: string;
  title: string;
  description: string;
}) {
  // Sentence case for the link; preserve acronyms such as MCP and API.
  const label = `View ${title.replace(/\b[A-Z][a-z]+\b/g, (word) => word.toLowerCase())} docs`;
  return (
    <HoverCard
      focusable={false}
      side="top"
      triggerClassName="rounded-sm"
      content={
        <>
          <HoverCardTitle>{title}</HoverCardTitle>
          <HoverCardBody>
            <p>{description}</p>
          </HoverCardBody>
        </>
      }
    >
      <a
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={`${label} (opens in a new tab)`}
        className={cn(
          "inline-flex h-7 items-center gap-1.5 rounded-sm px-2 text-label text-ink-muted hover:text-ink",
          focusRing,
        )}
      >
        {label}
        <ArrowUpRightIcon width={13} height={13} aria-hidden="true" />
      </a>
    </HoverCard>
  );
}
