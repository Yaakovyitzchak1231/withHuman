import { useEffect, useRef, useState } from "react";
import { cn } from "@/ds/lib/cn";

/*
 * The withHuman logo: the brand written as a tool call, withHuman(^),
 * with a person glyph as the argument. The person is a dot (head) over
 * a caret (shoulders), and the caret is also the text cursor, the
 * place where the human enters the loop. Pure outlined SVG, no logo
 * font ships; strictly monochrome in currentColor. Path geometry is
 * shared with the marketing site's lockup and must not be edited.
 * Size the whole lockup via font-size on className.
 *
 * The lockup can collapse into the W mark (`collapsed`), the same
 * two-beat animation the marketing nav plays on scroll: the name folds
 * away under the (^), then the (^) morphs into the W. The product uses
 * it for the sign-in splash (LogoSplash).
 */

const WORDMARK_D =
  "M0.44 11.9H2.86L4.53 19.75Q4.62 20.1 4.71 20.79L4.99 19.75L6.75 13.21H9.49L11.25 19.75L11.5 20.79L11.71 19.75L13.42 11.9H15.66L12.84 23.1H10.14L8.32 16.31L8.09 15.29Q8.02 15.73 7.83 16.31L5.98 23.1H3.3Z M17.6 21.16H20.19V13.84H18.06V11.9H22.59V21.16H24.83V23.1H17.6ZM19.91 7.25H22.66V10.05H19.91Z M29.59 19.1V13.84H26.36V11.9H29.59V8.02H31.99V11.9H35.67V13.84H31.99V18.8Q31.99 20.12 32.59 20.67Q33.19 21.23 34.56 21.23Q35.2 21.23 35.76 21.14V23.1Q34.97 23.31 34.05 23.31Q29.59 23.31 29.59 19.1Z M38.83 6.93H41.23V12.47Q41.23 13.1 41.19 13.44Q41.7 12.59 42.58 12.06Q43.47 11.53 44.7 11.53Q46.64 11.53 47.72 12.74Q48.81 13.95 48.81 16.08V23.1H46.41V16.4Q46.41 14.97 45.82 14.19Q45.23 13.42 44.03 13.42Q42.78 13.42 42.01 14.3Q41.23 15.18 41.23 16.47V23.1H38.83Z M51.95 6.93H54.52V13.63H61.47V6.93H64.03V23.1H61.47V15.8H54.52V23.1H51.95Z M67.34 18.99V11.9H69.74V18.55Q69.74 19.98 70.33 20.78Q70.92 21.58 72.12 21.58Q73.39 21.58 74.1 20.69Q74.82 19.8 74.82 18.27V11.9H77.22V23.1H74.84V22.52Q74.84 21.99 74.91 21.55Q74.45 22.43 73.57 22.95Q72.7 23.47 71.45 23.47Q69.53 23.47 68.43 22.3Q67.34 21.14 67.34 18.99Z M80.3 11.9H82.56V12.64Q82.56 13.19 82.54 13.44Q82.93 12.57 83.65 12.05Q84.36 11.53 85.33 11.53Q86.26 11.53 86.98 12.07Q87.71 12.61 88.03 13.61Q88.5 12.66 89.29 12.09Q90.09 11.53 91.15 11.53Q92.65 11.53 93.56 12.62Q94.46 13.72 94.46 15.66V23.1H92.17V16.03Q92.17 13.42 90.51 13.42Q89.54 13.42 89.02 14.24Q88.5 15.06 88.5 16.31V23.1H86.23V16.08Q86.23 13.42 84.57 13.42Q83.62 13.42 83.09 14.24Q82.56 15.06 82.56 16.35V23.1H80.3Z M97.02 20.03Q97.02 17.02 101.11 16.42L103.21 16.15Q103.86 16.05 104.12 15.79Q104.39 15.52 104.39 15.04Q104.39 14.28 103.82 13.81Q103.26 13.35 102.29 13.35Q101.13 13.35 100.47 13.92Q99.82 14.48 99.77 15.45H97.32Q97.39 14.35 97.99 13.46Q98.59 12.57 99.66 12.05Q100.74 11.53 102.15 11.53Q104.48 11.53 105.62 12.72Q106.77 13.91 106.77 16.1V23.1H104.53V22.59Q104.53 22.27 104.57 21.58Q104.13 22.45 103.2 22.96Q102.26 23.47 100.92 23.47Q99.86 23.47 98.96 23.05Q98.06 22.64 97.54 21.85Q97.02 21.07 97.02 20.03ZM104.44 18.8V17.37Q104.2 17.67 103.14 17.86L101.8 18.11Q100.69 18.32 100.09 18.75Q99.49 19.17 99.49 19.98Q99.49 20.77 100.05 21.22Q100.6 21.67 101.46 21.67Q102.73 21.67 103.58 20.87Q104.44 20.07 104.44 18.8Z M110.33 11.9H112.73V12.52Q112.73 12.75 112.68 13.44Q113.17 12.61 114.08 12.07Q114.99 11.53 116.19 11.53Q118.13 11.53 119.21 12.74Q120.28 13.95 120.28 16.08V23.1H117.88V16.4Q117.88 14.97 117.3 14.19Q116.72 13.42 115.55 13.42Q114.28 13.42 113.5 14.3Q112.73 15.18 112.73 16.47V23.1H110.33Z";

function Wordmark() {
  return (
    <svg
      viewBox="0 0 121.9 23.1"
      aria-hidden
      className="h-[1em] w-auto max-w-none shrink-0 overflow-visible"
    >
      <path d={WORDMARK_D} fill="currentColor" />
    </svg>
  );
}

/*
 * MorphMark: the lockup's (^), drawn so it can morph into the W mark.
 * State A is the (^) at the lockup's own metrics; state B is the W:
 * the parens straighten and splay into the outer arms, the caret rises
 * into the middle peak, and the dot head rides up. The path morph runs
 * as a rAF tween that lerps the coordinates and writes the `d`
 * attribute each frame, because WebKit never interpolates the CSS `d`
 * property. Stroke-width and the dot ride CSS transitions in
 * styles/logo.css on the same clock: MORPH_MS and MORPH_DELAY_MS here,
 * the .is-collapsed delays there, and the Tailwind delay/duration
 * classes in Logo must move together. Timings and geometry match the
 * marketing lockup exactly.
 */
const MORPH_MS = 250;
const MORPH_DELAY_MS = 470;

type MorphPathKey = "armL" | "armR" | "caret";

const fmtParen = (n: number[]) =>
  `M${n[0]} ${n[1]} C${n[2]} ${n[3]} ${n[4]} ${n[5]} ${n[6]} ${n[7]}`;
const fmtCaret = (n: number[]) =>
  `M${n[0]} ${n[1]} L${n[2]} ${n[3]} L${n[4]} ${n[5]}`;

const MORPH: Record<
  MorphPathKey,
  { a: number[]; b: number[]; fmt: (n: number[]) => string }
> = {
  armL: {
    a: [6.41, 7.05, 2.33, 11.09, 2.33, 21.23, 6.4, 25.27],
    b: [4.4, 3.6, 6.47, 9.53, 8.53, 15.47, 10.6, 21.4],
    fmt: fmtParen,
  },
  armR: {
    a: [28.29, 7.05, 32.37, 11.09, 32.37, 21.23, 28.3, 25.27],
    b: [29.6, 3.6, 27.53, 9.53, 25.47, 15.47, 23.4, 21.4],
    fmt: fmtParen,
  },
  caret: {
    a: [12.52, 22.35, 17.35, 16.79, 22.18, 22.35],
    b: [10.6, 21.4, 17, 11.8, 23.4, 21.4],
    fmt: fmtCaret,
  },
};

/* Path data at morph progress p: 0 is the (^), 1 is the W. */
function morphPathAt(key: MorphPathKey, p: number) {
  const { a, b, fmt } = MORPH[key];
  return fmt(a.map((v, i) => v + (b[i] - v) * p));
}

/* CSS ease-out, cubic-bezier(0, 0, 0.58, 1): solve x(u) = t by Newton
 * iteration, return y(u). Matches the CSS transitions running
 * alongside the path tween. */
function easeOut(t: number) {
  if (t <= 0) return 0;
  if (t >= 1) return 1;
  let u = t;
  for (let i = 0; i < 8; i++) {
    const x = (1.74 - 0.74 * u) * u * u - t;
    const dx = (3.48 - 2.22 * u) * u;
    if (dx < 1e-6) break;
    u = Math.min(1, Math.max(0, u - x / dx));
  }
  return (3 - 2 * u) * u * u;
}

function MorphMark({ collapsed, animate }: { collapsed: boolean; animate: boolean }) {
  const paths = useRef<Record<MorphPathKey, SVGPathElement | null>>({
    armL: null,
    armR: null,
    caret: null,
  });
  const progress = useRef(collapsed ? 1 : 0);
  /* The JSX `d` is frozen at the mount state so React never rewrites
   * the attribute mid-tween; after mount only the effect touches it. */
  const [mountProgress] = useState(() => (collapsed ? 1 : 0));

  useEffect(() => {
    const target = collapsed ? 1 : 0;
    if (progress.current === target) return;
    const apply = (p: number) => {
      progress.current = p;
      for (const key of Object.keys(paths.current) as MorphPathKey[]) {
        paths.current[key]?.setAttribute("d", morphPathAt(key, p));
      }
    };
    /* Hidden tab: rAF is suspended, so tweening would leave the paths
     * stranded at the old geometry. Jump to the end state, which is
     * what CSS transitions guarantee too. */
    if (
      !animate ||
      document.hidden ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      apply(target);
      return;
    }
    const from = progress.current;
    let raf = 0;
    let start: number | null = null;
    const frame = (now: number) => {
      start ??= now;
      const t = Math.min((now - start) / MORPH_MS, 1);
      apply(from + (target - from) * easeOut(t));
      if (t < 1) raf = requestAnimationFrame(frame);
    };
    /* Collapsing waits out the name fold plus a beat; expanding, the
     * morph leads. A reversal during the wait clears the timer and the
     * expand tween starts from wherever the paths are. */
    const timer = setTimeout(
      () => {
        raf = requestAnimationFrame(frame);
      },
      collapsed ? MORPH_DELAY_MS : 0,
    );
    return () => {
      clearTimeout(timer);
      cancelAnimationFrame(raf);
    };
  }, [collapsed, animate]);

  return (
    <svg
      viewBox="0 0 34 23.1"
      aria-hidden
      className={cn(
        "logo-morph h-[1em] w-auto max-w-none shrink-0 overflow-visible",
        collapsed && "is-collapsed",
      )}
    >
      <path
        ref={(el) => {
          paths.current.armL = el;
        }}
        className="mm-arm-l"
        d={morphPathAt("armL", mountProgress)}
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
      />
      <path
        ref={(el) => {
          paths.current.armR = el;
        }}
        className="mm-arm-r"
        d={morphPathAt("armR", mountProgress)}
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
      />
      <path
        ref={(el) => {
          paths.current.caret = el;
        }}
        className="mm-caret"
        d={morphPathAt("caret", mountProgress)}
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle className="mm-dot" cx="17.35" cy="10.94" r="2.34" fill="currentColor" />
    </svg>
  );
}

export interface LogoProps {
  /** Collapse the lockup into the W mark, animated. */
  collapsed?: boolean;
  /** Whether a change of `collapsed` tweens or snaps. */
  animate?: boolean;
  className?: string;
}

/* The CSS ease-out curve the JS path tween is fitted to; the design
 * system's own ease-out token is a different curve. */
const EASE = "ease-[cubic-bezier(0,0,0.58,1)]";

/*
 * The lockup: withHuman(^) in currentColor. Collapsing plays in two
 * beats:
 *
 *   1. The name folds away. "withHuman" clips from the right as its
 *      cell shrinks to zero width, so the (^) rides left, sweeping
 *      over the letters, until only (^) remains.
 *   2. After a short beat, the (^) morphs into the W mark.
 *
 * Expanding reverses the order: the W relaxes back into (^), then the
 * name unfolds. The fold and cell widths run on Tailwind transitions
 * here; the path morph lives in MorphMark and styles/logo.css.
 */
export function Logo({ collapsed = false, animate = true, className }: LogoProps) {
  return (
    <span
      role="img"
      aria-label="withHuman"
      className={cn("relative inline-flex h-[1.5em] items-end", className)}
    >
      {/* stage 1: the name's cell folds to zero width. 5.277em is
       * "withHuman"'s exact advance sum. The wordmark clips from the
       * right via clip-path with a negative vertical inset (not
       * overflow-hidden) so the round letters' baseline overshoot
       * survives. */}
      <span
        className={cn(
          "mb-[0.445em] shrink-0 [clip-path:inset(-1em_0)] transition-[width] duration-350",
          EASE,
          !animate && "transition-none",
          collapsed ? "w-0 delay-0" : "w-[5.277em] delay-[370ms]",
        )}
      >
        <Wordmark />
      </span>
      {/* stage 2: the mark's cell. The cell narrows 1.502em -> 1.472em
       * on the morph's clock (the (^) run is 0.03em wider than the W).
       * The mark keeps the wordmark's baseline in both states, unlike
       * the marketing nav which recenters the W in its pill: here the
       * (^) sweeps left on its own row and the W's feet land where the
       * caret's were, so nothing moves vertically but the morph. */}
      <span
        className={cn(
          "mb-[0.445em] flex items-baseline overflow-visible transition-[width] duration-250",
          EASE,
          !animate && "transition-none",
          collapsed ? "w-[1.472em] delay-[470ms]" : "w-[1.502em] delay-0",
        )}
      >
        <MorphMark collapsed={collapsed} animate={animate} />
      </span>
    </span>
  );
}
