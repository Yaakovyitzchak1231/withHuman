/* Nested containers alternate surface levels rather than stacking
 * identical fills: an odd depth sits on the surface, an even one on the
 * canvas. The escalation tree's wells and the pipeline's branch wells
 * share it, so nesting reads the same everywhere. */
export function wellTone(depth: number): "bg-surface" | "bg-canvas" {
  return depth % 2 === 0 ? "bg-canvas" : "bg-surface";
}
