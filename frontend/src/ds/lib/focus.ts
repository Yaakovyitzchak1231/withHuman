/*
 * Shared focus treatments. Keyboard-only (:focus-visible): a crisp 2px ring
 * offset just outside the control, no glow.
 */
export const focusRing =
  "outline-none focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-focus";

/* Inputs keep focus inside the border instead of an offset ring. */
export const focusWithin =
  "focus-within:border-accent focus-within:shadow-[0_0_0_2.5px_var(--ds-accent-subtle)]";
