export interface PipelineSearch {
  edit?: true;
  revision?: number;
  block?: string;
}

export function pipelineSearch(search: Record<string, unknown>): PipelineSearch {
  const revision = typeof search.revision === "number" || typeof search.revision === "string"
    ? Number(search.revision)
    : NaN;
  return {
    ...(search.edit === true || search.edit === "true" ? { edit: true as const } : {}),
    ...(Number.isSafeInteger(revision) && revision > 0 ? { revision } : {}),
    ...(typeof search.block === "string" && search.block.trim() ? { block: search.block } : {}),
  };
}

/* A pipeline's page by key. A request's links carry the revision that
 * ran, never today's active revision. A block key is scoped to that
 * pipeline and revision. */
export function pipelineLink(pipelineKey: string, search: PipelineSearch = {}) {
  return { to: "/pipelines/$pipelineKey" as const, params: { pipelineKey }, search };
}
