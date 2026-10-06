/* Nested containers alternate surface levels rather than stacking
 * identical fills: an odd depth sits on the surface, an even one on the
 * canvas. The escalation tree's wells use it; a pipeline branch's well
 * is a guide line beside its cards rather than a fill. */
export function wellTone(depth: number): "bg-surface" | "bg-canvas" {
  return depth % 2 === 0 ? "bg-canvas" : "bg-surface";
}
