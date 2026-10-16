import { useCallback, useMemo, useState } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { Button } from "@/ds/components/button";
import { Checklist, type ChecklistItem } from "@/ds/components/checklist";
import { IconButton } from "@/ds/components/icon-button";
import { Tooltip } from "@/ds/components/tooltip";
import { XIcon } from "@/ds/icons";
import type { SetupChecklist } from "../types";
import { useSession } from "./principal";

const DISMISSED_KEY = "wh-setup-reminder-dismissed";

function readDismissed(organization: string): boolean {
  try {
    return localStorage.getItem(`${DISMISSED_KEY}:${organization}`) === "1";
  } catch {
    return false;
  }
}

export interface SetupReminder {
  /* What the organization has set up and what is left, for a person who
   * can run the setup; empty when there is nothing to offer. */
  items: ChecklistItem[];
  /* The sidebar card is showing: an item is left, the card was not
   * hidden here, and the person is not on the setup itself. */
  visible: boolean;
  dismiss: () => void;
}

/* The setup is offered again until the organization has an agent
 * connected, approval rules written and an approval tested, however it
 * got there. Hiding the card is this browser's choice; the palette's
 * "Set up an agent on this machine" keeps the way back. */
export function useSetupReminder(): SetupReminder {
  const session = useSession();
  const { pathname } = useLocation();
  const organization = session.principal.organization_id;
  const checklist = session.onboarding?.checklist ?? null;
  const items = useMemo(() => (checklist ? checklistItems(checklist) : []), [checklist]);
  const [dismissed, setDismissed] = useState(() => readDismissed(organization));
  const dismiss = useCallback(() => {
    try {
      localStorage.setItem(`${DISMISSED_KEY}:${organization}`, "1");
    } catch {
      /* storage unavailable: hidden until reload */
    }
    setDismissed(true);
  }, [organization]);
  const left = items.some((item) => !item.done);
  return { items, visible: left && !dismissed && pathname !== "/welcome", dismiss };
}

function checklistItems(checklist: SetupChecklist): ChecklistItem[] {
  return [
    { label: "Connect an agent", done: checklist.agent_connected },
    { label: "Write approval rules", done: checklist.rules_written },
    { label: "Test an approval", done: checklist.tested },
  ];
}

/* The card above the account menu that brings the setup back. */
export function SetupReminderCard({ items, onDismiss }: { items: ChecklistItem[]; onDismiss: () => void }) {
  const navigate = useNavigate();
  return (
    <section
      aria-label="Finish setup"
      className="flex flex-col gap-2.5 rounded-lg border-hairline border-edge bg-surface p-3 shadow-sm"
    >
      <div className="flex items-start justify-between gap-2">
        <h2 className="text-body font-medium text-ink">Finish setup</h2>
        <Tooltip content="Hide">
          <IconButton aria-label="Hide" size="sm" className="-mt-1 -mr-1.5 shrink-0" onClick={onDismiss}>
            <XIcon width={13} height={13} />
          </IconButton>
        </Tooltip>
      </div>
      <Checklist items={items} />
      <Button
        variant="primary"
        size="sm"
        className="self-start"
        onClick={() => void navigate({ to: "/welcome" })}
      >
        Continue setup
      </Button>
    </section>
  );
}
