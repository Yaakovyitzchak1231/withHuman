import { useState } from "react";
import { Avatar } from "@/ds/components/avatar";
import { Badge, type Tone } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import { List, ListCell, ListRowContent } from "@/ds/components/list";
import { SelectionBar } from "@/ds/components/selection-bar";
import { useToast } from "@/ds/components/toast";
import { Section, Demo, KeyboardHint } from "../section";

interface Request {
  id: string;
  agent: string;
  summary: string;
  tool: string;
  status: "pending" | "approved" | "denied" | "expired";
  age: string;
}

const STATUS_TONE: Record<Request["status"], Tone> = {
  pending: "warn",
  approved: "approve",
  denied: "deny",
  expired: "neutral",
};

const REQUESTS: Request[] = [
  { id: "req_01", agent: "billing agent", summary: "Refund $420 to Acme Corp", tool: "stripe.refunds.create", status: "pending", age: "4m" },
  { id: "req_02", agent: "billing agent", summary: "Refund $89 to Northwind", tool: "stripe.refunds.create", status: "pending", age: "12m" },
  { id: "req_03", agent: "support agent", summary: "Update CRM owner for Initech", tool: "crm.contacts.update", status: "pending", age: "26m" },
  { id: "req_04", agent: "deploy bot", summary: "Deploy billing-service v2.4.1", tool: "deploy.release", status: "pending", age: "41m" },
  { id: "req_05", agent: "support agent", summary: "Merge duplicate accounts for Globex", tool: "crm.accounts.merge", status: "approved", age: "2h" },
  { id: "req_06", agent: "billing agent", summary: "Refund $1,200 to Stark Industries", tool: "stripe.refunds.create", status: "denied", age: "5h" },
  { id: "req_07", agent: "deploy bot", summary: "Rotate database credentials", tool: "vault.secrets.rotate", status: "approved", age: "1d" },
  { id: "req_08", agent: "support agent", summary: "Export contact list for audit", tool: "crm.contacts.export", status: "expired", age: "3d" },
];

export function ListsSection() {
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [lastOpened, setLastOpened] = useState<string | null>(null);
  const toast = useToast();

  function decide(kind: "approved" | "denied") {
    toast.add({
      type: kind === "approved" ? "approve" : "deny",
      title: `${selected.size} ${selected.size === 1 ? "request" : "requests"} ${kind}`,
    });
    setSelected(new Set());
  }

  return (
    <Section
      id="lists"
      title="List"
      lede="The queue surface. Hover and arrows share one highlight; nothing happens on a plain click. Selecting is deliberate (checkbox, x, shift ranges) and so is opening (View, Enter, double-click)."
    >
      <Demo
        label="Review queue"
        hint={lastOpened ? `opened: ${lastOpened}` : "select via checkbox; View opens"}
        bare
      >
        <div className="overflow-hidden rounded-lg border-hairline border-edge bg-surface shadow-sm">
          <List
            items={REQUESTS}
            getId={(request) => request.id}
            selected={selected}
            onSelectedChange={setSelected}
            onOpen={(request) => {
              setLastOpened(request.summary);
              toast.add({
                title: "Request opened",
                description: request.summary,
              });
            }}
            aria-label="Review queue"
          >
            {(request) => (
              <ListRowContent
                leading={
                  <Avatar
                    name={request.agent}
                    kind="agent"
                    size="sm"
                    tooltip={false}
                  />
                }
                title={request.summary}
                trailing={
                  <>
                    <ListCell
                      width={172}
                      className="hidden font-mono text-[12px] text-ink-muted sm:block"
                    >
                      {request.tool}
                    </ListCell>
                    <ListCell width={96}>
                      <Badge tone={STATUS_TONE[request.status]}>
                        {request.status}
                      </Badge>
                    </ListCell>
                    <ListCell
                      width={30}
                      align="right"
                      className="font-mono text-micro font-normal text-ink-faint"
                    >
                      {request.age}
                    </ListCell>
                  </>
                }
              />
            )}
          </List>
        </div>
        <KeyboardHint>
          Arrows move the highlight, x or Space or mod+click selects,
          shift extends a range, mod+A selects all, Esc clears, Enter or
          double-click opens.
        </KeyboardHint>
        <p className="mt-3 text-micro text-ink-faint">
          Use <code>renderRow(item, props)</code> to add row behavior, such as
          the review queue's route preloading on hover or focus. Forward the
          supplied props, children and ref to an <code>li</code>, composing
          added handlers with the list's handlers to retain its controls.
        </p>
      </Demo>

      <SelectionBar count={selected.size} onClear={() => setSelected(new Set())}>
        <Button size="sm" variant="danger" onClick={() => decide("denied")}>
          Deny
        </Button>
        <Button size="sm" variant="approve" onClick={() => decide("approved")}>
          Approve
        </Button>
      </SelectionBar>
    </Section>
  );
}
