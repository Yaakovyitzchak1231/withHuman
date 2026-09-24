/* What the new-presentation page can be opened with: the binding to
 * start from and a request whose arguments serve as the sample. Every
 * part is optional; a bare page starts empty. */
export interface NewToolPresentationSearch {
  tool?: string;
  runtime?: string;
  server?: string;
  request?: string;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : undefined;
}

export function validateNewToolPresentationSearch(search: Record<string, unknown>): NewToolPresentationSearch {
  const out: NewToolPresentationSearch = {};
  const tool = optionalString(search.tool);
  const runtime = optionalString(search.runtime);
  const server = optionalString(search.server);
  const request = optionalString(search.request);
  if (tool) out.tool = tool;
  if (runtime) out.runtime = runtime;
  if (server) out.server = server;
  if (request) out.request = request;
  return out;
}
