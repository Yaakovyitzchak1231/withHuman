import { useState } from "react";
import { Button } from "@/ds/components/button";
import {
  Dialog,
  DialogBody,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
  DialogTrigger,
} from "@/ds/components/dialog";
import { RevisionActivationDialog } from "@/ds/components/revision-activation-dialog";
import { TextField } from "@/ds/components/text-field";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function DialogsSection() {
  const [open, setOpen] = useState(false);
  const [revisionDemo, setRevisionDemo] = useState<"activate" | "draft_only" | "failure" | null>(null);

  return (
    <Section
      id="dialogs"
      title="Dialog"
      lede="The overlay surface. Focus is trapped, Esc closes, and the footer carries the deny and approve pairing."
    >
      <Demo label="Decision dialog">
        <DemoRow>
          <Dialog open={open} onOpenChange={setOpen}>
            <DialogTrigger render={<Button variant="primary">Review request</Button>} />
            <DialogContent>
              <DialogTitle>Approve this refund?</DialogTitle>
              <DialogDescription>
                billing-agent-01 wants to refund $420 to Acme Corp. This
                action posts to Stripe immediately.
              </DialogDescription>
              <DialogBody>
                <TextField
                  label="Note for the audit log"
                  placeholder="Optional context for this decision"
                />
              </DialogBody>
              <DialogFooter>
                <DialogClose render={<Button variant="ghost">Cancel</Button>} />
                <Button variant="danger" onClick={() => setOpen(false)}>
                  Deny
                </Button>
                <Button variant="approve" onClick={() => setOpen(false)}>
                  Approve
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </DemoRow>
        <KeyboardHint>
          Focus is trapped inside; Esc or the backdrop closes; focus returns
          to the trigger. Fields keep their full focus glow at the edges of
          the scrollable body.
        </KeyboardHint>
      </Demo>
      <Demo label="Saved revision">
        <DemoRow>
          <Button onClick={() => setRevisionDemo("activate")}>Revision saved</Button>
          <Button onClick={() => setRevisionDemo("draft_only")}>Without activation permission</Button>
          <Button onClick={() => setRevisionDemo("failure")}>Activation failure</Button>
        </DemoRow>
        <KeyboardHint>
          Save first, then choose Make active or Keep as draft. Dismissal keeps the draft.
          Activation failures leave the saved revision available for retry.
        </KeyboardHint>
        {revisionDemo ? (
          <RevisionActivationDialog
            revision={4}
            activeRevision={3}
            canActivate={revisionDemo !== "draft_only"}
            activationDescription="Once active, new requests use this revision."
            onKeepDraft={() => setRevisionDemo(null)}
            onActivate={async () => {
              if (revisionDemo === "failure") throw new Error("An escalation path has no active revision.");
              setRevisionDemo(null);
            }}
          />
        ) : null}
      </Demo>
    </Section>
  );
}
