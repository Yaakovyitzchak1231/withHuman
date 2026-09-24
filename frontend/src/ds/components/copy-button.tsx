import { useRef, useState } from "react";
import { CheckIcon, CopyIcon } from "@/ds/icons";
import { IconButton, type IconButtonProps } from "./icon-button";
import { Tooltip } from "./tooltip";

/* How long the confirmation pins the control visible. */
const PIN_MS = 1500;
/* The icon stays a tick until the wrapper's 100ms fade has finished,
 * so an unhovered button disappears as a tick, never flashing back to
 * the copy glyph. */
const ICON_REVERT_MS = PIN_MS + 250;

/* Clipboard write with the shared confirmation timing: `copied` drives
 * the tick or "Copied" label, `pinned` keeps hover-reveal wrappers open
 * while it shows. */
export function useCopyToClipboard(value: string) {
  const [copied, setCopied] = useState(false);
  const [pinned, setPinned] = useState(false);
  const timersRef = useRef<number[]>([]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      timersRef.current.forEach((id) => window.clearTimeout(id));
      setCopied(true);
      setPinned(true);
      timersRef.current = [
        window.setTimeout(() => setPinned(false), PIN_MS),
        window.setTimeout(() => setCopied(false), ICON_REVERT_MS),
      ];
    } catch {
      /* Clipboard unavailable (permissions, insecure context). */
    }
  }

  return { copy, copied, pinned };
}

export interface CopyButtonProps
  extends Omit<IconButtonProps, "aria-label" | "children"> {
  /* The text placed on the clipboard. */
  value: string;
  "aria-label"?: string;
}

export function CopyButton({
  value,
  "aria-label": ariaLabel = "Copy",
  size = "sm",
  ...props
}: CopyButtonProps) {
  const { copy, copied, pinned } = useCopyToClipboard(value);

  return (
    <Tooltip content={copied ? "Copied" : ariaLabel}>
      <IconButton
        aria-label={ariaLabel}
        size={size}
        onClick={copy}
        /* Lets hover-reveal wrappers stay visible while the tick shows. */
        data-copied={pinned || undefined}
        className={copied ? "text-approve hover:text-approve" : undefined}
        {...props}
      >
        {copied ? (
          <CheckIcon width={14} height={14} />
        ) : (
          <CopyIcon width={14} height={14} />
        )}
      </IconButton>
    </Tooltip>
  );
}
