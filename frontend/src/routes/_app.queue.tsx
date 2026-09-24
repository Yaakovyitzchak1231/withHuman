import { createFileRoute } from "@tanstack/react-router";
import { QueueScreen } from "../app/queue";
import { ensureQueue } from "../app/queue-store";
import { queueScopeOf, validateQueueSearch } from "../app/queue-search";

export const Route = createFileRoute("/_app/queue")({
  validateSearch: validateQueueSearch,
  loaderDeps: ({ search }) => ({ scope: queueScopeOf(search) }),
  /* The list renders with its first snapshot of the layer, never from
   * empty. */
  loader: ({ deps }) => ensureQueue(deps.scope),
  component: () => <QueueScreen openID={null} preloaded={null} />,
});
