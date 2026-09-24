import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";

/* The standard raised panel: surface, hairline, small shadow. */
export function Card({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "rounded-lg border-hairline border-edge bg-surface shadow-sm",
        className,
      )}
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex items-start justify-between gap-4 border-b-hairline border-edge px-5 py-4",
        className,
      )}
      {...props}
    />
  );
}

export function CardTitle({ className, ...props }: ComponentProps<"h3">) {
  return <h3 className={cn("text-body font-semibold text-ink", className)} {...props} />;
}

export function CardDescription({ className, ...props }: ComponentProps<"p">) {
  return (
    <p className={cn("mt-0.5 text-body text-ink-muted", className)} {...props} />
  );
}

export function CardBody({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("px-5 py-4", className)} {...props} />;
}

export function CardFooter({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex items-center justify-end gap-2.5 border-t-hairline border-edge px-5 py-3.5",
        className,
      )}
      {...props}
    />
  );
}
