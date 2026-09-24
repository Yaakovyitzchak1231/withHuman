import { useState } from "react";
import { Badge } from "@/ds/components/badge";
import { Checkbox } from "@/ds/components/checkbox";
import { Switch } from "@/ds/components/switch";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function TogglesSection() {
  const [notify, setNotify] = useState(true);
  const [selected, setSelected] = useState<boolean[]>([true, false, false]);

  const checkedCount = selected.filter(Boolean).length;
  const allChecked = checkedCount === selected.length;
  const indeterminate = checkedCount > 0 && !allChecked;

  return (
    <Section
      id="toggles"
      title="Switch & Checkbox"
      lede="The switch thumb travels in 160ms; the checkmark draws itself in. Indeterminate serves bulk selection in queues."
    >
      <Demo label="Switch" hint="wired to the badge">
        <DemoRow className="gap-8">
          <Switch
            label="Notify me on new requests"
            checked={notify}
            onCheckedChange={setNotify}
          />
          <Badge tone={notify ? "approve" : "neutral"}>
            {notify ? "Subscribed" : "Muted"}
          </Badge>
          <Switch label="Disabled" disabled />
        </DemoRow>
        <KeyboardHint>Space toggles; the label is clickable.</KeyboardHint>
      </Demo>

      <Demo label="Checkbox" hint="parent shows indeterminate">
        <div className="flex max-w-[320px] flex-col gap-1">
          <div className="border-b-hairline border-edge px-2 pb-2">
            <Checkbox
              label={`Select all (${checkedCount}/${selected.length})`}
              checked={allChecked}
              indeterminate={indeterminate}
              onCheckedChange={(checked) =>
                setSelected(selected.map(() => checked === true))
              }
            />
          </div>
          {["Refund $420 to Acme Corp", "Update CRM owner field", "Deploy billing-service v2"].map(
            (labelText, index) => (
              <div key={labelText} className="px-2 pt-1">
                <Checkbox
                  label={labelText}
                  checked={selected[index]}
                  onCheckedChange={(checked) =>
                    setSelected(
                      selected.map((value, i) =>
                        i === index ? checked === true : value,
                      ),
                    )
                  }
                />
              </div>
            ),
          )}
        </div>
        <KeyboardHint>Space toggles each box.</KeyboardHint>
      </Demo>
    </Section>
  );
}
