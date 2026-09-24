import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/ds/lib/cn";

export interface EmptyStateProps extends ComponentProps<"div"> {
  /* 16px icon shown inside the hairline ring. */
  icon?: ReactNode;
  title: string;
  description?: string;
  /* Usually a Button. */
  action?: ReactNode;
}

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
  ...props
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center px-6 py-12 text-center",
        className,
      )}
      {...props}
    >
      {icon ? (
        <span className="mb-4 flex size-10 items-center justify-center rounded-full border-hairline border-edge text-ink-muted">
          {icon}
        </span>
      ) : null}
      <p className="text-body font-medium text-ink">{title}</p>
      {description ? (
        <p className="mt-1 max-w-[340px] text-body text-ink-muted">
          {description}
        </p>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  );
}
