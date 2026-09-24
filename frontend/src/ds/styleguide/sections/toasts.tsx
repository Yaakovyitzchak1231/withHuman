import { Button } from "@/ds/components/button";
import { useToast } from "@/ds/components/toast";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function ToastsSection() {
  const toast = useToast();

  return (
    <Section
      id="toasts"
      title="Toast"
      lede="Bottom-right stack: newest in front, older ones peek out behind, hover expands. Select the title or message to copy it. Swipe outside the text or use the close button to dismiss."
    >
      <Demo label="Fire notifications" hint="stack a few to see the peek">
        <DemoRow>
          <Button
            variant="approve"
            onClick={() =>
              toast.add({
                type: "approve",
                title: "Request approved",
                description: "The refund posted to Stripe.",
              })
            }
          >
            Approve toast
          </Button>
          <Button
            variant="danger"
            onClick={() =>
              toast.add({
                type: "deny",
                title: "Request denied",
                description: "billing-agent-01 was notified.",
              })
            }
          >
            Deny toast
          </Button>
          <Button
            onClick={() =>
              toast.add({
                type: "warn",
                title: "Request expiring soon",
                description: "Refund $420 to Acme Corp expires in 5 minutes.",
              })
            }
          >
            Warning toast
          </Button>
          <Button
            variant="ghost"
            onClick={() =>
              toast.add({ title: "Agent connected" })
            }
          >
            Plain toast
          </Button>
        </DemoRow>
        <KeyboardHint>
          F6 focuses the stack; arrows move between toasts; Esc dismisses the
          focused one.
        </KeyboardHint>
      </Demo>
    </Section>
  );
}
