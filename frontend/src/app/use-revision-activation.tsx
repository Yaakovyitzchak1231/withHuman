import { useState } from "react";
import { RevisionActivationDialog } from "@/ds/components/revision-activation-dialog";
import { useToast } from "@/ds/components/toast";
import { RequestError } from "../api";

interface SavedRevision {
  revision: number;
  activeRevision: number | null;
  canActivate: boolean;
  activationDescription: string;
  /* A draft offered for activation as it was, with nothing saved. */
  unchanged?: boolean;
  activate: () => Promise<unknown>;
}

/* Saving a draft in place is refused once anyone has activated it. */
export function isRevisionNotDraft(reason: unknown): boolean {
  return reason instanceof RequestError && reason.status === 409 && reason.code === "revision_not_draft";
}

/* Every revisioned save pauses here before refreshing or navigating away.
 * Activation retries use the saved revision, never create another one. */
export function useRevisionActivation() {
  const toast = useToast();
  const [pending, setPending] = useState<(SavedRevision & { resolve: () => void }) | null>(null);

  function offer(saved: SavedRevision): Promise<void> {
    return new Promise((resolve) => setPending({ ...saved, resolve }));
  }

  function finish(active: boolean) {
    if (!pending) return;
    /* Keeping an untouched draft changed nothing, so there is nothing to say. */
    if (active || !pending.unchanged) {
      toast.add({
        type: active ? "approve" : "neutral",
        title: `Revision ${pending.revision} ${active ? "active" : "saved as draft"}`,
        description: active
          ? pending.activationDescription
          : "Activate it later to make your changes live.",
      });
    }
    setPending(null);
    pending.resolve();
  }

  const dialog = pending ? (
    <RevisionActivationDialog
      revision={pending.revision}
      activeRevision={pending.activeRevision}
      canActivate={pending.canActivate}
      activationDescription={pending.activationDescription}
      unchanged={pending.unchanged}
      onKeepDraft={() => finish(false)}
      onActivate={async () => {
        try {
          await pending.activate();
        } catch (reason) {
          if (reason instanceof RequestError && reason.status === 412) {
            throw new Error("Someone else changed the active revision. Keep this draft and review the page first.");
          }
          throw reason;
        }
        finish(true);
      }}
    />
  ) : null;

  /* A save to a draft someone activated meanwhile: says so and answers
   * true. The caller keeps the editor open so the work can be copied. */
  function refusedDraft(reason: unknown): boolean {
    if (!isRevisionNotDraft(reason)) return false;
    toast.add({
      type: "deny",
      title: "Draft already activated",
      description: "Someone else activated it. Your changes were not saved.",
    });
    return true;
  }

  return { offer, dialog, refusedDraft };
}
