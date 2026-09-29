// Schema metadata is advisory. This module never rewrites a condition or
// resolves remote references, and array paths stop at the array itself.
export interface ToolSchema {
  name: string;
  source: string;
  description?: string;
  inputSchema?: Record<string, unknown>;
  observedPaths?: string[];
  observedValues?: Record<string, (string | number)[]>;
  /* A server's arguments rather than one tool's: every request through
   * the server carries them, so they apply whichever tool a condition
   * names. */
  everyTool?: boolean;
}
export interface SchemaField {
  path: string;
  types: string[];
  description: string;
  required: boolean;
  values: (string | number | boolean | null)[];
}
export interface FieldHint extends SchemaField {
  sources: string[];
  partial: boolean;
  unknown: boolean;
}
interface Condition {
  id: string; field?: string; operator?: string; value?: unknown;
  all?: Condition[]; any?: Condition[];
}
const escape = (key: string) => key.replace(/~/g, "~0").replace(/\//g, "~1");
const object = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
const primitive = (value: unknown): value is string | number | boolean | null => value === null || ["string", "number", "boolean"].includes(typeof value);

export function schemaFields(schema?: Record<string, unknown>): SchemaField[] {
  if (!schema) return [];
  const fields: SchemaField[] = [];
  function resolve(value: Record<string, unknown>, refs: Set<string>): Record<string, unknown> | undefined {
    if (typeof value.$ref !== "string") return value;
    if ((value.$ref !== "#" && !value.$ref.startsWith("#/")) || refs.has(value.$ref)) return undefined;
    let target: unknown = schema;
    if (value.$ref !== "#") for (const key of value.$ref.slice(2).split("/")) target = object(target)?.[key.replace(/~1/g, "/").replace(/~0/g, "~")];
    const resolved = object(target);
    if (!resolved) return undefined;
    refs.add(value.$ref);
    const base = resolve(resolved, refs);
    if (!base) return undefined;
    const siblings = Object.fromEntries(Object.entries(value).filter(([key]) => key !== "$ref"));
    // Overlapping constraints are an intersection, not an override. Keep that
    // combination in the full schema rather than inventing a simplified type.
    if (Object.keys(siblings).some((key) => key !== "description" && key !== "title" && key in base && JSON.stringify(base[key]) !== JSON.stringify(siblings[key]))) return undefined;
    return { ...base, ...siblings };
  }
  function walk(raw: Record<string, unknown>, path: string, required: boolean, depth: number, refs: Set<string>) {
    if (depth > 24 || fields.length >= 2000) return;
    const chain = new Set(refs);
    const node = resolve(raw, chain);
    const complex = !node || ["oneOf", "anyOf", "allOf", "if", "not"].some((key) => key in node);
    const types = !complex ? (Array.isArray(node.type) ? node.type.filter((type): type is string => typeof type === "string") : typeof node.type === "string" ? [node.type] : []) : [];
    const values = node && !complex ? (Array.isArray(node.enum) ? node.enum.filter(primitive) : "const" in node && primitive(node.const) ? [node.const] : []) : [];
    if (!types.length && values.length && !complex) types.push(...new Set(values.map((value) => value === null ? "null" : typeof value)));
    if (path !== "/request/arguments") fields.push({ path, types, values, required, description: typeof node?.description === "string" ? node.description : "" });
    if (complex || types.includes("array")) return;
    const properties = object(node.properties);
    if (!properties) return;
    const requiredKeys = Array.isArray(node.required) ? node.required : [];
    for (const [key, value] of Object.entries(properties)) {
      const child = object(value);
      if (child) walk(child, `${path}/${escape(key)}`, required && requiredKeys.includes(key), depth + 1, chain);
    }
  }
  walk(schema, "/request/arguments", true, 0, new Set());
  return fields;
}

type Names = Set<string> | null;
function intersect(a: Names, b: Names): Names {
  if (a === null) return b;
  if (b === null) return a;
  return new Set([...a].filter((name) => b.has(name)));
}
function constraint(node: Condition): Names {
  if (node.field === "/request/tool") {
    if (node.operator === "equals" && typeof node.value === "string" && node.value) return new Set([node.value]);
    if (node.operator === "in" && Array.isArray(node.value) && node.value.every((item) => typeof item === "string")) return new Set(node.value);
  }
  if (node.all) return node.all.reduce<Names>((result, child) => intersect(result, constraint(child)), null);
  if (node.any) {
    const alternatives = node.any.map(constraint);
    if (alternatives.some((item) => item === null)) return null;
    return new Set(alternatives.flatMap((item) => [...item!]));
  }
  return null;
}
export function applicableToolNames(root: Condition, id: string): Names {
  function find(node: Condition, inherited: Names): Names | undefined {
    if (node.id === id) return inherited;
    const shared = node.all ? intersect(inherited, constraint(node)) : inherited;
    for (const child of node.all ?? node.any ?? []) {
      const found = find(child, shared);
      if (found !== undefined) return found;
    }
    return undefined;
  }
  return find(root, null) ?? null;
}
export function fieldHints(root: Condition, id: string, tools: ToolSchema[]): { fields: Record<string, FieldHint>; tools: ToolSchema[]; narrowed: boolean } {
  const names = applicableToolNames(root, id);
  const selected = tools.filter((tool) => tool.everyTool || names === null || names.has(tool.name));
  const fields: Record<string, FieldHint> = {};
  const counts = new Map<string, number>();
  for (const tool of selected) {
    const declared = schemaFields(tool.inputSchema);
    const byPath = new Map(declared.map((field) => [field.path, field]));
    for (const path of tool.observedPaths ?? []) if (!byPath.has(path)) byPath.set(path, { path, types: [], description: "Observed in a recent request", required: false, values: [] });
    for (const field of byPath.values()) {
      const existing = fields[field.path];
      counts.set(field.path, (counts.get(field.path) ?? 0) + 1);
      const values = [...field.values, ...(tool.observedValues?.[field.path] ?? [])];
      fields[field.path] = {
        ...field,
        types: [...new Set([...(existing?.types ?? []), ...field.types])],
        values: [...new Set([...(existing?.values ?? []), ...values])],
        description: [...new Set([existing?.description, field.description].filter(Boolean))].join(" · "),
        required: field.required && (existing?.required ?? true),
        sources: [...new Set([...(existing?.sources ?? []), `${tool.name} (${tool.source})`])],
        partial: false,
        unknown: (existing?.unknown ?? false) || !field.types.length,
      };
    }
  }
  for (const field of Object.values(fields)) {
    field.partial = counts.get(field.path) !== selected.length;
    field.required &&= !field.partial;
    field.unknown ||= field.partial;
  }
  return { fields, tools: selected, narrowed: names !== null };
}
const presence = ["exists", "not_exists", "is_null", "is_not_null"];
export function suitableOperators(field?: FieldHint): string[] | null {
  if (!field || field.unknown || field.types.filter((type) => type !== "null").length !== 1) return null;
  switch (field.types.find((type) => type !== "null")) {
    case "number": case "integer": return ["equals", "not_equals", "less_than", "less_than_or_equal", "greater_than", "greater_than_or_equal", ...presence];
    case "boolean": return ["equals", "not_equals", "is_true", "is_false", ...presence];
    case "string": return ["equals", "not_equals", "in", "not_in", "contains", "not_contains", "starts_with", "ends_with", "regex", "is_empty", "is_not_empty", ...presence];
    case "array": return ["contains", "not_contains", "contains_any", "contains_all", "is_empty", "is_not_empty", ...presence];
    case "object": return ["is_empty", "is_not_empty", ...presence];
    default: return null;
  }
}
export function schemaWarning(node: Condition, hints: ReturnType<typeof fieldHints>): string | undefined {
  if (!node.field?.startsWith("/request/arguments/")) return;
  const field = hints.fields[node.field];
  if (!field) return hints.narrowed && hints.tools.length && hints.tools.every((tool) => tool.inputSchema) ? "This field is not declared in the current tool schemas. Check the definition before using this condition." : undefined;
  const operators = suitableOperators(field);
  if (operators && node.operator && !operators.includes(node.operator)) return "This operator does not match the current argument type. The saved condition is unchanged.";
  if (field.unknown) return;
  const values = node.operator === "in" || node.operator === "not_in" ? (Array.isArray(node.value) ? node.value : []) : [node.value];
  const comparison = ["equals", "not_equals", "in", "not_in", "less_than", "less_than_or_equal", "greater_than", "greater_than_or_equal"].includes(node.operator ?? "");
  const matchesType = (value: unknown) => field.types.some((type) => type === "null" ? value === null : type === "integer" ? typeof value === "number" && Number.isInteger(value) : typeof value === type);
  if (comparison && values.some((value) => value !== undefined && !matchesType(value))) return "The comparison value has a different type from the current argument schema.";
  if (["equals", "not_equals", "in", "not_in"].includes(node.operator ?? "")) {
    const definitions = hints.tools.map((tool) => schemaFields(tool.inputSchema).find((item) => item.path === node.field));
    if (definitions.every((item) => item && item.values.length) && values.some((value) => !definitions.some((item) => item!.values.includes(value as string | number | boolean | null)))) return "The comparison value is outside the allowed values in the current tool schemas. The saved condition is unchanged.";
  }
}
