import { cn } from "@/ds/lib/cn";

const IS_MAC =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

const GLYPHS: Record<string, string | { mac: string; other: string }> = {
  mod: { mac: "⌘", other: "Ctrl" },
  cmd: "⌘",
  ctrl: "Ctrl",
  shift: "⇧",
  alt: { mac: "⌥", other: "Alt" },
  enter: "↵",
  esc: "Esc",
  tab: "Tab",
  space: "Space",
  backspace: "⌫",
  up: "↑",
  down: "↓",
  left: "←",
  right: "→",
};

function glyph(key: string): string {
  const hit = GLYPHS[key.toLowerCase()];
  if (!hit) return key.length === 1 ? key.toUpperCase() : key;
  if (typeof hit === "string") return hit;
  return IS_MAC ? hit.mac : hit.other;
}

export interface KbdProps {
  /* Shortcut like "mod+K" or "shift+enter"; "mod" renders per platform. */
  keys: string;
  /* inverse sits on inverted surfaces like tooltips. */
  tone?: "default" | "inverse";
  className?: string;
}

export function Kbd({ keys, tone = "default", className }: KbdProps) {
  return (
    <span className={cn("inline-flex items-center gap-[3px]", className)}>
      {keys.split("+").map((key, index) => (
        <kbd
          key={index}
          className={cn(
            "inline-flex h-[18px] min-w-[18px] items-center justify-center",
            "rounded-xs border-hairline px-1",
            "font-mono text-micro font-normal",
            tone === "default"
              ? "border-edge bg-fill-active text-ink-secondary shadow-[0_1px_0_var(--ds-edge)]"
              : "border-current/25 bg-transparent text-current",
          )}
        >
          {glyph(key)}
        </kbd>
      ))}
    </span>
  );
}
