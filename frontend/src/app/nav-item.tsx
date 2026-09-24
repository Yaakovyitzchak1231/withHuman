import type { ReactNode } from "react";
import { Link, type LinkProps } from "@tanstack/react-router";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

/* One sidebar entry. The router marks the anchor with
 * data-status="active"; a parent path stays active on its children via
 * fuzzy matching (/queue on /queue/$requestId). */
export function NavItem({
  to,
  icon,
  children,
}: {
  to: LinkProps["to"];
  icon?: ReactNode;
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
      {children}
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
