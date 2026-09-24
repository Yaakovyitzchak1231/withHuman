import { useState } from "react";
import { useToast } from "@/ds/components/toast";
import type { RevisionPage } from "../types";

/*
 * Pages of a revision history. The route loader fetches the first page;
 * Load more appends the following ones. The extra pages belong to the
 * loader's page: a save or an activation reloads the route, which brings
 * a new first page and drops them, so the history never shows a row
 * twice or a stale active mark.
 */
export function useRevisionPages<Item>(
  first: RevisionPage<Item>,
  fetchPage: (cursor: string) => Promise<RevisionPage<Item>>,
) {
  const toast = useToast();
  const [more, setMore] = useState<{
    base: RevisionPage<Item>;
    revisions: Item[];
    nextCursor: string;
  } | null>(null);
  const extended = more?.base === first ? more : null;
  const [loadingMore, setLoadingMore] = useState(false);
  const items = extended ? [...first.revisions, ...extended.revisions] : first.revisions;
  const nextCursor = extended ? extended.nextCursor : (first.next_cursor ?? "");

  async function loadMore() {
    if (!nextCursor || loadingMore) return;
    setLoadingMore(true);
    try {
      const next = await fetchPage(nextCursor);
      setMore({
        base: first,
        revisions: [...(extended?.revisions ?? []), ...next.revisions],
        nextCursor: next.next_cursor ?? "",
      });
    } catch (reason) {
      toast.add({
        type: "deny",
        title: "More revisions could not be loaded",
        description: reason instanceof Error ? reason.message : undefined,
      });
    } finally {
      setLoadingMore(false);
    }
  }

  return {
    items,
    totalCount: first.total_count,
    hasMore: nextCursor !== "",
    loadMore: () => void loadMore(),
    loadingMore,
  };
}

/* The page a loader hands over when the history could not be read: no
 * rows, nothing more to fetch. */
export function emptyRevisionPage<Item>(): RevisionPage<Item> {
  return { revisions: [], total_count: 0 };
}
