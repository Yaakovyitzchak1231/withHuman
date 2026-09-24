import { Kbd } from "@/ds/components/kbd";
import { Section, Demo, DemoRow } from "../section";

export function KbdSection() {
  return (
    <Section
      id="kbd"
      title="Kbd"
      lede="Keyboard affordances appear anywhere a shortcut exists: buttons, menu items, tooltips. The mod key renders per platform."
    >
      <Demo label="Shortcuts">
        <DemoRow className="gap-6">
          <Kbd keys="mod+K" />
          <Kbd keys="mod+enter" />
          <Kbd keys="shift+alt+D" />
          <Kbd keys="esc" />
          <Kbd keys="up" />
          <Kbd keys="G" />
        </DemoRow>
      </Demo>

      <Demo label="In context">
        <div className="flex max-w-[360px] flex-col gap-2">
          {[
            { action: "Approve selected", keys: "A" },
            { action: "Deny selected", keys: "D" },
            { action: "Open command menu", keys: "mod+K" },
          ].map((row) => (
            <div
              key={row.action}
              className="flex items-center justify-between rounded-sm px-2 py-1.5 hover:bg-fill-hover"
            >
              <span className="text-body text-ink">{row.action}</span>
              <Kbd keys={row.keys} />
            </div>
          ))}
        </div>
      </Demo>
    </Section>
  );
}
