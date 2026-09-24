import { useState } from "react";
import { Button, type ButtonVariant } from "@/ds/components/button";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

const VARIANTS: { variant: ButtonVariant; label: string }[] = [
  { variant: "primary", label: "New request" },
  { variant: "neutral", label: "View details" },
  { variant: "ghost", label: "Dismiss" },
  { variant: "approve", label: "Approve" },
  { variant: "danger", label: "Deny" },
];

export function ButtonsSection() {
  const [loading, setLoading] = useState(false);

  function simulate() {
    setLoading(true);
    window.setTimeout(() => setLoading(false), 1800);
  }

  return (
    <Section
      id="buttons"
      title="Button"
      lede="Five variants on three sizes. Presses compress by half a pixel instead of bouncing; hovers settle within 100ms."
    >
      <Demo label="Variants" hint="md size">
        <DemoRow>
          {VARIANTS.map(({ variant, label }) => (
            <Button key={variant} variant={variant}>
              {label}
            </Button>
          ))}
        </DemoRow>
        <KeyboardHint>Tab focuses, Enter or Space activates.</KeyboardHint>
      </Demo>

      <Demo label="Sizes">
        <DemoRow>
          <Button size="sm">Small · 26</Button>
          <Button size="md">Medium · 30</Button>
          <Button size="lg">Large · 36</Button>
        </DemoRow>
      </Demo>

      <Demo
        label="States"
        hint="loading keeps the button width; click Approve to simulate"
      >
        <DemoRow>
          <Button disabled>Disabled</Button>
          <Button variant="primary" disabled>
            Disabled primary
          </Button>
          <Button variant="approve" loading={loading} onClick={simulate}>
            Approve request
          </Button>
        </DemoRow>
      </Demo>

      <Demo label="Decision pairing" hint="the product's core moment">
        <div className="flex max-w-[420px] flex-col gap-3">
          <p className="text-body text-ink-secondary">
            billing-agent-01 wants to refund $420 to Acme Corp.
          </p>
          <div className="grid grid-cols-2 gap-2.5">
            <Button variant="danger">Deny</Button>
            <Button variant="approve">Approve</Button>
          </div>
        </div>
      </Demo>
    </Section>
  );
}
