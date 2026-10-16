import { Checklist } from "@/ds/components/checklist";
import { Section, Demo } from "../section";

export function ChecklistsSection() {
  return (
    <Section
      id="checklists"
      title="Checklist"
      lede="Things to do in any order. A done item gets the check node and recedes to muted ink, so what is left reads first. Use a Stepper when the steps follow one another."
    >
      <Demo label="Setup" hint="one done, two left">
        <Checklist
          className="max-w-[240px]"
          items={[
            { label: "Connect an agent", done: true },
            { label: "Write approval rules", done: false },
            { label: "Test an approval", done: false },
          ]}
        />
      </Demo>
    </Section>
  );
}
