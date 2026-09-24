import type { ConditionValueSuggestions } from "@/ds/components/condition-builder";

/* Array elements are candidates for membership comparisons at the array's
 * path. Objects themselves are never offered as scalar values. */
export function sampleValueSuggestions(sampleText: string): ConditionValueSuggestions {
  const result: ConditionValueSuggestions = {};
  function walk(value: unknown, path: string) {
    if (typeof value === "string" || typeof value === "number") {
      if (value !== "") result[path] = [...new Set([...(result[path] ?? []), value])];
    } else if (Array.isArray(value)) {
      for (const item of value) {
        if (typeof item === "string" || typeof item === "number") walk(item, path);
      }
    } else if (value !== null && typeof value === "object") {
      for (const [key, child] of Object.entries(value)) {
        walk(child, `${path}/${key.replace(/~/g, "~0").replace(/\//g, "~1")}`);
      }
    }
  }
  try {
    walk(JSON.parse(sampleText), "");
  } catch {
    // An incomplete test request must not hide the directory's suggestions.
  }
  return result;
}
