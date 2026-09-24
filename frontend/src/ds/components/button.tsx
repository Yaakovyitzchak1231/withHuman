import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

export type ButtonVariant = "primary" | "neutral" | "ghost" | "approve" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

/* Hover on translucent-fill states swaps the wash; hover on solid fills
 * shades with a fixed dark overlay so it works over any accent hue. */
const SOLID_HOVER =
  "hover:[background-image:linear-gradient(rgba(0,0,0,0.09),rgba(0,0,0,0.09))] active:[background-image:linear-gradient(rgba(0,0,0,0.16),rgba(0,0,0,0.16))]";

const VARIANTS: Record<ButtonVariant, string> = {
  primary: cn("bg-accent text-ink-on-accent", SOLID_HOVER),
  neutral: cn(
    "border-hairline border-edge bg-control text-ink shadow-sm",
    "hover:border-edge-strong",
    "hover:[background-image:linear-gradient(var(--ds-fill-hover),var(--ds-fill-hover))]",
    "active:shadow-none active:[background-image:linear-gradient(var(--ds-fill-active),var(--ds-fill-active))]",
  ),
  ghost: cn(
    "text-ink-secondary",
    "hover:bg-fill-hover hover:text-ink",
    "active:bg-fill-active",
  ),
  approve: cn("bg-approve text-ink-on-accent", SOLID_HOVER),
  danger: cn("bg-deny text-ink-on-accent", SOLID_HOVER),
};

const SIZES: Record<ButtonSize, string> = {
  sm: "h-[26px] rounded-sm px-2.5 text-label",
  md: "h-[30px] rounded-sm px-3 text-label",
  lg: "h-9 rounded-md px-3.5 text-body font-medium",
};

export interface ButtonProps extends ComponentProps<"button"> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
}

export function Button({
  variant = "neutral",
  size = "md",
  loading = false,
  disabled,
  className,
  children,
  ...props
}: ButtonProps) {
  return (
    <button
      type="button"
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cn(
        "inline-grid cursor-pointer place-items-center select-none whitespace-nowrap",
        "transition-[color,background-color,border-color,box-shadow,transform] duration-100 ease-standard",
        "active:translate-y-[0.5px]",
        !loading && "disabled:pointer-events-none disabled:opacity-50",
        loading && "pointer-events-none",
        focusRing,
        VARIANTS[variant],
        SIZES[size],
        className,
      )}
      {...props}
    >
      <span
        className={cn(
          "col-start-1 row-start-1 inline-flex items-center justify-center gap-1.5",
          loading && "opacity-0",
        )}
      >
        {children}
      </span>
      {loading ? (
        <span
          aria-hidden
          className="col-start-1 row-start-1 inline-flex items-center justify-center gap-1"
        >
          {[0, 1, 2].map((dot) => (
            <span
              key={dot}
              className="size-1 rounded-full bg-current animate-[ds-dot_1.2s_ease-in-out_infinite]"
              style={{ animationDelay: `${dot * 160}ms` }}
            />
          ))}
        </span>
      ) : null}
    </button>
  );
}
