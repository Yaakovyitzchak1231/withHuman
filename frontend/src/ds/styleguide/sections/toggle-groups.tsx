import { useState } from "react";
import { ToggleGroup, ToggleGroupItem } from "@/ds/components/toggle-group";
import { ThemeToggle } from "@/ds/theme/theme-toggle";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function ToggleGroupsSection() {
  const [filter, setFilter] = useState("pending");

  return (
    <Section
      id="toggle-groups"
      title="ToggleGroup"
      lede="A segmented control with exactly one segment always pressed. The theme segments in the sidebar's account menu wear these clothes inside a Dropdown."
    >
      <Demo label="Icon segments" hint="a live theme picker">
        <DemoRow className="gap-6">
          <ThemeToggle />
        </DemoRow>
      </Demo>

      <Demo label="Text segments" hint={`showing: ${filter}`}>
        <DemoRow className="gap-6">
          <ToggleGroup
            aria-label="Queue filter"
            value={filter}
            onValueChange={setFilter}
          >
            <ToggleGroupItem value="all">All</ToggleGroupItem>
            <ToggleGroupItem value="pending">Pending</ToggleGroupItem>
            <ToggleGroupItem value="resolved">Resolved</ToggleGroupItem>
          </ToggleGroup>
          <ToggleGroup aria-label="Disabled example" value="a" onValueChange={() => {}} disabled>
            <ToggleGroupItem value="a">One</ToggleGroupItem>
            <ToggleGroupItem value="b">Two</ToggleGroupItem>
          </ToggleGroup>
        </DemoRow>
        <KeyboardHint>
          Arrows move between segments; Enter or Space presses the focused
          one. Pressing the active segment again keeps it active.
        </KeyboardHint>
      </Demo>
    </Section>
  );
}
