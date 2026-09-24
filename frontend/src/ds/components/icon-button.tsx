import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

export type IconButtonVariant = "ghost" | "neutral";
export type IconButtonSize = "sm" | "md";

const VARIANTS: Record<IconButtonVariant, string> = {
  ghost: cn(
    "text-ink-muted",
    "hover:bg-fill-hover hover:text-ink",
    "active:bg-fill-active",
  ),
  neutral: cn(
    "border-hairline border-edge bg-control text-ink-secondary shadow-sm",
    "hover:border-edge-strong hover:text-ink",
    "hover:[background-image:linear-gradient(var(--ds-fill-hover),var(--ds-fill-hover))]",
    "active:shadow-none active:[background-image:linear-gradient(var(--ds-fill-active),var(--ds-fill-active))]",
  ),
};

const SIZES: Record<IconButtonSize, string> = {
  sm: "size-[26px] rounded-sm",
  md: "size-[30px] rounded-sm",
};

export interface IconButtonProps extends ComponentProps<"button"> {
  /* Icon-only controls always describe themselves. */
  "aria-label": string;
  variant?: IconButtonVariant;
  size?: IconButtonSize;
}

export function IconButton({
  variant = "ghost",
  size = "md",
  className,
  children,
  ...props
}: IconButtonProps) {
  return (
    <button
      type="button"
      className={cn(
        "inline-flex cursor-pointer items-center justify-center select-none",
        "transition-[color,background-color,border-color,box-shadow,transform] duration-100 ease-standard",
        "active:translate-y-[0.5px]",
        "disabled:pointer-events-none disabled:opacity-50",
        focusRing,
        VARIANTS[variant],
        SIZES[size],
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}
