import { useState } from "react";
import { RevisionHistory, type RevisionRow } from "@/ds/components/revision-history";
import { useToast } from "@/ds/components/toast";
import { Section, Demo, KeyboardHint } from "../section";

/* A path's history, deep enough to page: 23 revisions, five first and
 * more on each Load more. */
interface DemoRevision extends RevisionRow {
  name: string;
  url: string;
}

const TOTAL = 23;
const PAGE = 5;
const AUTHORS: Record<string, string> = {
  m1: "Ana Batista",
  m2: "Sam Okafor",
  m3: "Priya Nair",
};
const NAMES = ["Fraud review", "Fraud review (EU)", "Fraud review, weekend", "Fraud review, night shift"];

function revisionAt(number: number, active: number): DemoRevision {
  const authors = Object.keys(AUTHORS);
  return {
    id: `rev_${number}`,
    revision: number,
    is_active: number === active,
    name: NAMES[number % NAMES.length],
    url: `https://hooks.acme.test/fraud/v${number}`,
    created_by_actor_id: authors[number % authors.length],
    created_at: new Date(Date.UTC(2026, 8, 1, 9, 0) - (TOTAL - number) * 26 * 3_600_000).toISOString(),
  };
}

function pageBelow(before: number, active: number): DemoRevision[] {
  const rows: DemoRevision[] = [];
  for (let number = before - 1; number >= 1 && rows.length < PAGE; number--) {
    rows.push(revisionAt(number, active));
  }
  return rows;
}

export function RevisionHistorySection() {
  const toast = useToast();
  const [active, setActive] = useState(21);
  const [activating, setActivating] = useState<number | null>(null);
  const [items, setItems] = useState(() => pageBelow(TOTAL + 1, 21));
  const [loadingMore, setLoadingMore] = useState(false);
  const last = items[items.length - 1]?.revision ?? 1;
  const hasMore = last > 1;

  function loadMore() {
    setLoadingMore(true);
    setTimeout(() => {
      setItems((current) => [...current, ...pageBelow(last, active)]);
      setLoadingMore(false);
    }, 500);
  }

  function activate(item: DemoRevision) {
    setActivating(item.revision);
    setTimeout(() => {
      setActive(item.revision);
      setItems((current) =>
        current.map((row) => ({ ...row, is_active: row.revision === item.revision })),
      );
      setActivating(null);
      toast.add({
        type: "approve",
        title:
          item.revision < active
            ? `Rolled back to revision ${item.revision} (demo)`
            : `Revision ${item.revision} active (demo)`,
      });
    }, 500);
  }

  return (
    <Section
      id="revision-history"
      title="RevisionHistory"
      lede="Every revisioned resource (pipelines, escalation paths, webhook endpoints) lists its history through this one table: fixed columns, the active mark, a row menu with Activate, Roll back and Create new revision from this, and cursor paging with Load more. Entries the reader cannot take stay in the menu faded, so every row reads the same. The screen supplies an optional summary column and what each action does."
    >
      <Demo
        label="Webhook endpoint history"
        hint="click a row to open it; the row menu activates, rolls back (earlier revisions only), or starts a new revision from the row; Load more fetches the next page"
      >
        <div className="overflow-hidden rounded-lg border-hairline border-edge bg-surface shadow-sm">
          <RevisionHistory
            aria-label="Demo revisions"
            info="Every save creates a new revision. Only the active revision runs; roll back to any earlier one at any time."
            description="Newest first. Activating an earlier revision rolls the endpoint back."
            items={items}
            totalCount={TOTAL}
            hasMore={hasMore}
            loadingMore={loadingMore}
            onLoadMore={loadMore}
            summaryLabel="Destination"
            summary={(item) => (
              <>
                <span className="text-ink" title={item.name}>
                  {item.name}
                </span>
                <span className="ml-2 font-mono text-[11px] text-ink-muted" title={item.url}>
                  {item.url}
                </span>
              </>
            )}
            authorName={(membershipId) => AUTHORS[membershipId]}
            activeRevision={active}
            onOpen={(item) => toast.add({ title: `Opened revision ${item.revision} (demo)` })}
            onActivate={activate}
            activating={activating}
            onCreateFrom={(item) =>
              toast.add({ title: `Editing from revision ${item.revision} (demo)` })
            }
            menuActions={(item) => [
              {
                id: "test",
                label: "Send test delivery",
                action: () => toast.add({ title: `Test delivery for revision ${item.revision} (demo)` }),
              },
            ]}
          />
        </div>
        <KeyboardHint>Arrows move, Enter opens the highlighted revision.</KeyboardHint>
      </Demo>
      <Demo
        label="Read-only history"
        hint="a reader without activate or write permission: the menu is still there, every entry faded"
      >
        <div className="overflow-hidden rounded-lg border-hairline border-edge bg-surface shadow-sm">
          <RevisionHistory
            aria-label="Read-only demo revisions"
            description="Kept for the record; this reader can only look."
            items={pageBelow(TOTAL + 1, 21).slice(0, 3)}
            totalCount={3}
            hasMore={false}
            summaryLabel="Destination"
            summary={(item) => <span title={item.name}>{item.name}</span>}
            authorName={(membershipId) => AUTHORS[membershipId]}
            activeRevision={21}
            onOpen={(item) => toast.add({ title: `Opened revision ${item.revision} (demo)` })}
          />
        </div>
      </Demo>
      <Demo label="Pipeline history" hint="revision, status and creation date, with no summary column">
        <div className="overflow-hidden rounded-lg border-hairline border-edge bg-surface shadow-sm">
          <RevisionHistory
            aria-label="Pipeline demo revisions"
            items={pageBelow(TOTAL + 1, 21).slice(0, 3)}
            totalCount={3}
            hasMore={false}
            activeRevision={21}
            onOpen={(item) => toast.add({ title: `Opened revision ${item.revision} (demo)` })}
          />
        </div>
      </Demo>
      <Demo
        label="Revived history"
        hint="a pipeline archived with its agent and revived when the agent is restored: the archived revisions stay readable, and the menu never activates or rolls back to them"
      >
        <div className="overflow-hidden rounded-lg border-hairline border-edge bg-surface shadow-sm">
          <RevisionHistory
            aria-label="Revived demo revisions"
            description="Revision 3 started fresh; revisions 1 and 2 are the archived history."
            items={[
              revisionAt(3, 3),
              { ...revisionAt(2, 3), archived_at: "2026-09-04T09:00:00Z" },
              { ...revisionAt(1, 3), archived_at: "2026-09-04T09:00:00Z" },
            ]}
            totalCount={3}
            hasMore={false}
            summaryLabel="Destination"
            summary={(item) => <span title={item.name}>{item.name}</span>}
            authorName={(membershipId) => AUTHORS[membershipId]}
            activeRevision={3}
            onOpen={(item) => toast.add({ title: `Opened revision ${item.revision} (demo)` })}
            onActivate={(item) => toast.add({ title: `Revision ${item.revision} active (demo)` })}
            onCreateFrom={(item) =>
              toast.add({ title: `Editing from revision ${item.revision} (demo)` })
            }
          />
        </div>
      </Demo>
      <Demo label="Empty history" hint="a key with no revisions yet">
        <div className="overflow-hidden rounded-lg border-hairline border-edge bg-surface shadow-sm">
          <RevisionHistory
            aria-label="Empty demo revisions"
            items={[] as DemoRevision[]}
            totalCount={0}
            hasMore={false}
            summaryLabel="Name"
            summary={(item) => item.name}
            activeRevision={null}
          />
        </div>
      </Demo>
    </Section>
  );
}
