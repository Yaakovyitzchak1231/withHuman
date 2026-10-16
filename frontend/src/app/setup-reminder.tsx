import { useCallback, useState } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { Button } from "@/ds/components/button";
import { IconButton } from "@/ds/components/icon-button";
import { Tooltip } from "@/ds/components/tooltip";
import { XIcon } from "@/ds/icons";
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
  /* The organization skipped the guided setup, nobody has finished it
   * since, and this person can run it. */
  skipped: boolean;
  /* The sidebar card is showing: skipped, not dismissed here, and the
   * person is not on the setup itself. */
  visible: boolean;
  dismiss: () => void;
}

/* A skipped setup is offered again until someone finishes it. Hiding
 * the card is this browser's choice; the palette's "Set up an agent on
 * this machine" keeps the way back. */
export function useSetupReminder(): SetupReminder {
  const session = useSession();
  const { pathname } = useLocation();
  const organization = session.principal.organization_id;
  const skipped = session.onboarding?.skipped === true;
  const [dismissed, setDismissed] = useState(() => readDismissed(organization));
  const dismiss = useCallback(() => {
    try {
      localStorage.setItem(`${DISMISSED_KEY}:${organization}`, "1");
    } catch {
      /* storage unavailable: hidden until reload */
    }
    setDismissed(true);
  }, [organization]);
  return { skipped, visible: skipped && !dismissed && pathname !== "/welcome", dismiss };
}

/* The card above the account menu that brings a skipped setup back. */
export function SetupReminderCard({ onDismiss }: { onDismiss: () => void }) {
  const navigate = useNavigate();
  return (
    <section
      aria-label="Finish setup"
      className="flex flex-col gap-2 rounded-lg border-hairline border-edge bg-surface p-3 shadow-sm"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex flex-col gap-0.5">
          <h2 className="text-body font-medium text-ink">Finish setup</h2>
          <p className="text-label font-normal text-ink-muted">Connect an agent and test an approval.</p>
        </div>
        <Tooltip content="Hide">
          <IconButton aria-label="Hide" size="sm" className="-mt-1 -mr-1.5 shrink-0" onClick={onDismiss}>
            <XIcon width={13} height={13} />
          </IconButton>
        </Tooltip>
      </div>
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
