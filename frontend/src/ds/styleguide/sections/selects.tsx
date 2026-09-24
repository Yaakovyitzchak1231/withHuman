import { useState } from "react";
import {
  MultiSelectTrigger,
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
} from "@/ds/components/select";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

interface Policy {
  label: string;
  value: string;
}

const POLICIES: Policy[] = [
  { label: "Always require approval", value: "always" },
  { label: "Approve under $100", value: "under-100" },
  { label: "Approve under $1,000", value: "under-1000" },
  { label: "Trusted, log only", value: "log-only" },
];

const CHANNELS: Policy[] = [
  { label: "Slack", value: "slack" },
  { label: "Email", value: "email" },
  { label: "Discord", value: "discord" },
  { label: "Pager", value: "pager" },
  { label: "Webhook", value: "webhook" },
];

interface PolicyGroup {
  value: string;
  items: Policy[];
}

const GROUPED: PolicyGroup[] = [
  { value: "Policies", items: POLICIES },
  { value: "Channels", items: CHANNELS },
];

export function SelectsSection() {
  const [policy, setPolicy] = useState<Policy | null>(POLICIES[0]);
  const [channels, setChannels] = useState<Policy[]>([
    CHANNELS[0],
    CHANNELS[1],
  ]);
  const [grouped, setGrouped] = useState<Policy | null>(null);

  return (
    <Section
      id="selects"
      title="Select"
      lede="A TextField-shaped trigger opening a menu-shaped list. Start typing while it is open and a filter row appears; the checkmark marks the current value."
    >
      <Demo
        label="Approval policy"
        hint={policy ? `value: ${policy.value}` : "nothing selected"}
      >
        <DemoRow className="gap-6">
          <Select
            items={POLICIES}
            itemToStringLabel={(item) => (item as Policy).label}
            value={policy}
            onValueChange={(value) => setPolicy(value as Policy | null)}
          >
            <SelectTrigger placeholder="Choose a policy" />
            <SelectContent<Policy>>
              {(item) => (
                <SelectItem key={item.value} value={item}>
                  {item.label}
                </SelectItem>
              )}
            </SelectContent>
          </Select>
          <Select items={POLICIES} disabled>
            <SelectTrigger placeholder="Disabled" />
          </Select>
        </DemoRow>
        <KeyboardHint>
          Arrows move, Enter selects, Esc closes, typing filters the options.
        </KeyboardHint>
      </Demo>

      <Demo
        label="Visible filter"
        hint="filterPlaceholder keeps the filter row open from the start; use for long vocabularies where filtering is the expected way in."
      >
        <DemoRow>
          <Select
            items={POLICIES}
            itemToStringLabel={(item) => (item as Policy).label}
            value={policy}
            onValueChange={(value) => setPolicy(value as Policy | null)}
          >
            <SelectTrigger placeholder="Choose a policy" />
            <SelectContent<Policy> filterPlaceholder="Filter policies">
              {(item) => (
                <SelectItem key={item.value} value={item}>
                  {item.label}
                </SelectItem>
              )}
            </SelectContent>
          </Select>
        </DemoRow>
      </Demo>

      <Demo
        label="Multiple"
        hint={`selected: ${channels.map((c) => c.value).join(", ") || "none"}`}
      >
        <DemoRow>
          <Select
            multiple
            items={CHANNELS}
            itemToStringLabel={(item) => (item as Policy).label}
            value={channels}
            onValueChange={(value) => setChannels(value as Policy[])}
          >
            <MultiSelectTrigger<Policy>
              placeholder="Notify channels"
              itemLabel={(item) => item.label}
            />
            <SelectContent<Policy>>
              {(item) => (
                <SelectItem key={item.value} value={item}>
                  {item.label}
                </SelectItem>
              )}
            </SelectContent>
          </Select>
          <Select
            multiple
            items={CHANNELS}
            itemToStringLabel={(item) => (item as Policy).label}
            value={channels}
            onValueChange={(value) => setChannels(value as Policy[])}
          >
            <MultiSelectTrigger<Policy>
              emptyLabel="All channels"
              itemLabel={(item) => item.label}
            />
            <SelectContent<Policy>>
              {(item) => (
                <SelectItem key={item.value} value={item}>
                  {item.label}
                </SelectItem>
              )}
            </SelectContent>
          </Select>
        </DemoRow>
        <KeyboardHint>
          The list stays open while toggling; Enter or click toggles each
          option, Esc closes. The second is a filter: nothing selected means
          everything, so its empty label reads as a value, not a prompt.
        </KeyboardHint>
      </Demo>

      <Demo
        label="Selected pills"
        hint="Selected values wrap inside the trigger. Toggle an option in the menu to remove it."
      >
        <div className="max-w-[400px]">
          <Select
            multiple
            items={CHANNELS}
            itemToStringLabel={(item) => (item as Policy).label}
            value={channels}
            onValueChange={(value) => setChannels(value as Policy[])}
          >
            <MultiSelectTrigger<Policy>
              display="pills"
              className="w-full"
              placeholder="Choose channels"
              itemLabel={(item) => item.label}
            />
            <SelectContent<Policy>>
              {(item) => (
                <SelectItem key={item.value} value={item}>
                  {item.label}
                </SelectItem>
              )}
            </SelectContent>
          </Select>
        </div>
      </Demo>

      <Demo
        label="Grouped"
        hint={grouped ? `value: ${grouped.value}` : "nothing selected"}
      >
        <DemoRow>
          <Select
            items={GROUPED}
            itemToStringLabel={(item) => (item as Policy).label}
            value={grouped}
            onValueChange={(value) => setGrouped(value as Policy | null)}
          >
            <SelectTrigger placeholder="Choose one" />
            <SelectContent<PolicyGroup>>
              {(group) => (
                <SelectGroup<Policy>
                  key={group.value}
                  items={group.items}
                  label={group.value}
                >
                  {(item) => (
                    <SelectItem key={item.value} value={item}>
                      {item.label}
                    </SelectItem>
                  )}
                </SelectGroup>
              )}
            </SelectContent>
          </Select>
        </DemoRow>
        <KeyboardHint>
          Pass the items as groups of `{"{ value, items }"}`; the filter row
          searches across every group.
        </KeyboardHint>
      </Demo>
    </Section>
  );
}
