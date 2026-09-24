import { type ComponentProps, type ReactNode } from "react";
import { createLink } from "@tanstack/react-router";
import { ArrowLeftIcon } from "@/ds/icons";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

const BACK_STYLE = "inline-flex items-center gap-1.5 text-label text-ink-muted hover:text-ink";

/** Page navigation always occupies its own row above the title and actions. */
export function PageHeader({
  back,
  children,
  className,
  ...props
}: ComponentProps<"div"> & { back?: ReactNode }) {
  return (
    <div className={cn("flex flex-col gap-3", className)} data-page-back={back ? "" : undefined} {...props}>
      {back ? <div className="pl-(--page-header-offset)">{back}</div> : null}
      {children}
    </div>
  );
}

function BackAnchor({ children, className, ...props }: ComponentProps<"a">) {
  return (
    <a className={cn(BACK_STYLE, focusRing, className)} {...props}>
      <ArrowLeftIcon width={14} height={14} aria-hidden="true" />
      {children}
    </a>
  );
}

export const PageBackLink = createLink(BackAnchor);

/** For page navigation that must run a callback, such as leaving an editor. */
export function PageBackButton({ children, className, ...props }: ComponentProps<"button">) {
  return (
    <button type="button" className={cn(BACK_STYLE, focusRing, className)} {...props}>
      <ArrowLeftIcon width={14} height={14} aria-hidden="true" />
      {children}
    </button>
  );
}
