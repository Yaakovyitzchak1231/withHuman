import type { ReactNode } from "react";
import { Link, type LinkProps } from "@tanstack/react-router";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

/* The most a count pill spells out; anything beyond it reads "99+", so
 * the pill never widens past three characters. */
const COUNT_CAP = 99;

export function formatNavCount(count: number): string {
  return count > COUNT_CAP ? `${COUNT_CAP}+` : String(count);
}

/* One sidebar entry. The router marks the anchor with
 * data-status="active"; a parent path stays active on its children via
 * fuzzy matching (/queue on /queue/$requestId). A count is the number
 * of things waiting behind the entry; zero shows nothing. */
export function NavItem({
  to,
  icon,
  count = 0,
  countLabel = "waiting",
  children,
}: {
  to: LinkProps["to"];
  icon?: ReactNode;
  count?: number;
  /* What the count is a count of, for assistive technology. */
  countLabel?: string;
  children: ReactNode;
}) {
  return (
    <Link
      to={to}
      className={cn(
        "group/nav flex h-8 cursor-pointer items-center gap-2.5 rounded-sm px-2",
        "text-body select-none",
        "transition-colors duration-100",
        "text-ink-secondary hover:bg-fill-hover hover:text-ink",
        "data-[status=active]:bg-fill-selected data-[status=active]:text-ink",
        focusRing,
      )}
    >
      {icon ? (
        <span className="text-ink-muted group-data-[status=active]/nav:text-accent">
          {icon}
        </span>
      ) : null}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {count > 0 ? (
        <span
          data-testid="nav-count"
          className={cn(
            "inline-flex h-[18px] min-w-[18px] shrink-0 items-center justify-center",
            "rounded-full bg-accent px-1.5 text-micro font-medium tabular-nums text-ink-on-accent",
          )}
        >
          <span aria-hidden="true">{formatNavCount(count)}</span>
          <span className="sr-only">{count} {countLabel}</span>
        </span>
      ) : null}
    </Link>
  );
}

/* A small uppercase heading over a group of NavItems. */
export function NavGroupLabel({ children }: { children: ReactNode }) {
  return (
    <p className="px-2 pt-4 pb-1 text-micro tracking-[0.08em] text-ink-faint uppercase">
      {children}
    </p>
  );
}
