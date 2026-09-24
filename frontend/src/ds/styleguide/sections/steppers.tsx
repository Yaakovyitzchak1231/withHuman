import { Stepper, type Step } from "@/ds/components/stepper";
import { Section, Demo } from "../section";

const SETUP: Step[] = [
  { title: "Install the CLI", description: "One command on the machine the agent runs on.", status: "done" },
  { title: "Choose agents", description: "Pick the runtimes the CLI found.", status: "done" },
  { title: "Choose tools", description: "Decide which calls wait for a human.", status: "current" },
  { title: "Test it", description: "Trigger a gated call and decide it here.", status: "upcoming" },
];

export function SteppersSection() {
  return (
    <Section
      id="steppers"
      title="Stepper"
      lede="Progress through a guided flow. Numbered nodes join by hairlines. The vertical rail sits beside the flow's content, with a labeled Badge (Done, Current, or Next) and a description per step. The horizontal band sits across the top of a screen the flow has to itself and carries titles only: the check, the filled current node, and the title weight say where the flow is."
    >
      <Demo label="Horizontal band" hint="two done, one current">
        <Stepper
          orientation="horizontal"
          steps={SETUP.map((step, index) => ({
            title: step.title,
            status: step.status,
            onSelect: index < 2 ? () => undefined : undefined,
          }))}
          className="max-w-[680px]"
        />
      </Demo>

      <Demo label="Vertical rail" hint="two done, one current">
        <Stepper steps={SETUP} className="max-w-[360px]" />
      </Demo>

      <Demo label="Titles only" hint="all done">
        <Stepper
          steps={SETUP.map((step, index) => ({
            title: step.title,
            status: "done",
            onSelect: index < 2 ? () => undefined : undefined,
          }))}
          className="max-w-[360px]"
        />
      </Demo>
    </Section>
  );
}
