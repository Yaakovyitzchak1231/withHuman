import { useState, type ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";
import { HoverCard } from "./hover-card";
import { useReferenceCard } from "./reference";
import { Tooltip } from "./tooltip";

export type AvatarKind = "human" | "agent";
export type AvatarSize = "sm" | "md" | "lg";

const SIZES: Record<AvatarSize, string> = {
  sm: "size-5 text-[9px]",
  md: "size-6 text-[10px]",
  lg: "size-7 text-[11px]",
};

/* Fixed decorative hues; humans get a deterministic tint from their
 * name. light-dark() follows the theme via color-scheme. */
const HUES = [25, 85, 145, 205, 265, 325];

function hueFor(name: string): number {
  let hash = 0;
  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) | 0;
  }
  return HUES[Math.abs(hash) % HUES.length];
}

function initialsOf(name: string): string {
  const words = name.trim().split(/[\s-]+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[words.length - 1][0]).toUpperCase();
}

export interface AvatarProps extends ComponentProps<"span"> {
  name: string;
  /* The membership id or agent slug behind the face, when the caller
   * has it, so the card on hover is about exactly this one. */
  id?: string;
  /* Profile picture; the tinted initials remain the fallback while it
   * loads or if it fails. */
  src?: string;
  /* Agents are square and mono; humans are round and tinted. */
  kind?: AvatarKind;
  size?: AvatarSize;
  /* The avatar explains itself on hover: the person's or agent's
   * reference card when the app provides one, else the name in a
   * tooltip. Pass false when the name already sits beside it and the
   * pair, not the face, is what should open the card. */
  tooltip?: boolean;
}

export function Avatar({
  name,
  id,
  src,
  kind = "human",
  size = "md",
  tooltip = true,
  className,
  style,
  ...props
}: AvatarProps) {
  const hue = hueFor(name);
  const [imageFailed, setImageFailed] = useState(false);
  const showImage = Boolean(src) && !imageFailed;
  const card = useReferenceCard(
    kind === "agent" ? { kind: "agent", slug: id ?? "", name } : { kind: "person", id, name },
  );

  const avatar = (
    <span
      style={
        kind === "human"
          ? {
              /* Opaque tint (hue mixed into the surface) so stacked
               * avatars cover each other cleanly. */
              backgroundColor: `color-mix(in oklab, var(--ds-surface) 82%, oklch(0.62 0.13 ${hue}))`,
              color: `light-dark(oklch(0.45 0.13 ${hue}), oklch(0.78 0.11 ${hue}))`,
              ...style,
            }
          : style
      }
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center overflow-hidden font-medium select-none",
        SIZES[size],
        kind === "human"
          ? "rounded-full"
          : "rounded-[5px] bg-fill-active font-mono text-ink-muted ring-hairline",
        className,
      )}
      {...props}
    >
      {initialsOf(name)}
      {showImage ? (
        <img
          src={src}
          alt=""
          onError={() => setImageFailed(true)}
          className="absolute inset-0 size-full object-cover"
        />
      ) : null}
    </span>
  );

  if (!tooltip) return avatar;
  if (card !== null) {
    return (
      <HoverCard
        content={card}
        /* The face fills its trigger, so the bubble is a halo around it. */
        triggerClassName={cn(
          "hover:ring-[3px] hover:ring-fill-hover data-popup-open:ring-[3px] data-popup-open:ring-fill-hover",
          kind === "agent" && "rounded-[5px]",
        )}
      >
        {avatar}
      </HoverCard>
    );
  }
  return <Tooltip content={name}>{avatar}</Tooltip>;
}
