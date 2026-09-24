import { cn } from "@/ds/lib/cn";
import { ChevronLeftIcon, ChevronRightIcon } from "@/ds/icons";
import { Button } from "./button";

/*
 * The footer of a paged table: which rows are on screen, how many
 * exist in all, and Previous and Next. The page index is controlled by
 * the screen (it usually lives in the URL), and the component never
 * assumes the page size, so the same footer closes a server-paged
 * table and one paged in memory.
 */
export interface PaginationProps {
  /* Zero-based page index. */
  page: number;
  pageSize: number;
  /* Rows on the current page. */
  count: number;
  /* Rows across every page, when known. */
  total?: number;
  hasMore: boolean;
  busy?: boolean;
  onChange: (page: number) => void;
  className?: string;
}

export function Pagination({
  page,
  pageSize,
  count,
  total,
  hasMore,
  busy = false,
  onChange,
  className,
}: PaginationProps) {
  const first = page * pageSize + 1;
  const last = page * pageSize + count;
  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-between gap-3 border-t-hairline border-edge px-4 py-3",
        className,
      )}
    >
      <p className="text-label font-normal text-ink-muted" aria-live="polite">
        {count
          ? `${first.toLocaleString()}–${last.toLocaleString()}`
          : "No results"}
        {total !== undefined && count > 0 ? ` of ${total.toLocaleString()}` : ""}
      </p>
      <div className="flex items-center gap-2">
        <Button size="sm" disabled={busy || page === 0} onClick={() => onChange(page - 1)}>
          <ChevronLeftIcon width={14} height={14} />
          Previous
        </Button>
        <span className="text-label font-normal text-ink-muted">Page {page + 1}</span>
        <Button size="sm" disabled={busy || !hasMore} onClick={() => onChange(page + 1)}>
          Next
          <ChevronRightIcon width={14} height={14} />
        </Button>
      </div>
    </div>
  );
}
