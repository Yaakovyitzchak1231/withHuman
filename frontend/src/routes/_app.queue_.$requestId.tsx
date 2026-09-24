import { createFileRoute } from "@tanstack/react-router";
import { api } from "../api";
import { QueueScreen } from "../app/queue";
import { ensureQueue, preloadReview } from "../app/queue-store";
import { queueScopeOf, validateQueueSearch } from "../app/queue-search";
import type { ApprovalRequest } from "../types";

export const Route = createFileRoute("/_app/queue_/$requestId")({
  validateSearch: validateQueueSearch,
  loaderDeps: ({ search }) => ({ scope: queueScopeOf(search) }),
  /* Everything the detail panel shows arrives together: the queue
   * snapshot of the layer in view, the request itself when the snapshot
   * does not carry it (automatic decisions, older history reached from
   * the audit log, and requests outside the layer), and the review
   * context. */
  loader: async ({ params, deps }): Promise<ApprovalRequest | null> => {
    const [, , fetched] = await Promise.all([
      ensureQueue(deps.scope),
      preloadReview(params.requestId),
      api.approval(params.requestId).catch(() => null),
    ]);
    return fetched;
  },
  component: QueueDetail,
});

function QueueDetail() {
  const { requestId } = Route.useParams();
  const preloaded = Route.useLoaderData();
  return <QueueScreen openID={requestId} preloaded={preloaded} />;
}
