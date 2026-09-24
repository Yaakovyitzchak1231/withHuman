import { useEffect, useRef, useState } from "react";
import { cn } from "@/ds/lib/cn";
import { Logo } from "./logo";

/*
 * Brand splash: a full-screen canvas with the logo centered, which
 * holds for a beat, plays the lockup's collapse or expansion, lingers,
 * and fades out over whatever mounted beneath it. "expand" starts
 * from the W and unfolds into withHuman(^) (sign-in); "collapse"
 * starts from the full withHuman(^) and ends on the W mark (sign-out).
 * Plays once per mount and reports back through onDone so the owner
 * can unmount it. Under reduced motion it shows the end state briefly
 * and leaves without any tween.
 */

export type LogoSplashDirection = "collapse" | "expand";

/* The beats, in ms: the start state holds, the lockup moves, the end
 * state lingers, then the canvas fades. MOVE_MS is the lockup's own
 * transition length in either direction: a 350ms fold and a 250ms
 * morph, separated by a beat, overlapping into 720ms. */
const HOLD_MS = 700;
const MOVE_MS = 720;
const LINGER_MS = 800;
const FADE_MS = 250;
const REDUCED_HOLD_MS = 600;

type Phase = "start" | "end" | "leaving";

export interface LogoSplashProps {
  direction?: LogoSplashDirection;
  onDone: () => void;
  className?: string;
}

export function LogoSplash({ direction = "expand", onDone, className }: LogoSplashProps) {
  const [reduced] = useState(
    () => window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  const [phase, setPhase] = useState<Phase>(reduced ? "end" : "start");
  const done = useRef(onDone);
  done.current = onDone;

  useEffect(() => {
    const leaveAt = reduced ? REDUCED_HOLD_MS : HOLD_MS + MOVE_MS + LINGER_MS;
    const timers = [
      setTimeout(() => setPhase("end"), HOLD_MS),
      setTimeout(() => setPhase("leaving"), leaveAt),
      setTimeout(() => done.current(), leaveAt + FADE_MS),
    ];
    return () => timers.forEach(clearTimeout);
  }, [reduced]);

  const atStart = phase === "start";
  const collapsed = direction === "collapse" ? !atStart : atStart;

  return (
    <div
      aria-hidden
      className={cn(
        "ds-root fixed inset-0 z-50 flex items-center justify-center bg-canvas",
        "transition-opacity duration-250 ease-out",
        phase === "leaving" ? "opacity-0" : "opacity-100",
        className,
      )}
    >
      <Logo collapsed={collapsed} animate={!reduced} className="text-[28px] text-ink" />
    </div>
  );
}
