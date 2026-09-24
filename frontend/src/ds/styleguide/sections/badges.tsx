import { useState } from "react";
import { Badge } from "@/ds/components/badge";
import {
  AlertIcon,
  CalendarIcon,
  PersonIcon,
  ProgressIcon,
} from "@/ds/icons";
import { Section, Demo, DemoRow } from "../section";

export function BadgesSection() {
  const [lastClicked, setLastClicked] = useState<string | null>(null);

  return (
    <Section
      id="badges"
      title="Badges"
      lede="Property pills: the icon carries the color, the label stays ink, and the rounded fill appears only on hover. Status is always a labeled badge with a status icon, never a bare dot: a dot asks the reader to memorize a color code, an icon draws the state and the badge says the word."
    >
      <Demo
        label="Properties"
        hint={
          lastClicked ? `clicked: ${lastClicked}` : "hover for the fill, click to select"
        }
      >
        <DemoRow className="gap-1">
          {[
            { label: "Active", tone: "warn" as const, icon: <ProgressIcon width={14} height={14} /> },
            { label: "Urgent", tone: "deny" as const, icon: <AlertIcon width={14} height={14} /> },
            { label: "Owner", tone: "neutral" as const, icon: <PersonIcon width={14} height={14} /> },
            { label: "Target date", tone: "neutral" as const, icon: <CalendarIcon width={14} height={14} /> },
          ].map((prop) => (
            <Badge
              key={prop.label}
              tone={prop.tone}
              icon={prop.icon}
              onClick={() => setLastClicked(prop.label)}
            >
              {prop.label}
            </Badge>
          ))}
        </DemoRow>
      </Demo>

      <Demo label="Request lifecycle" hint="default icon is the tone's status icon: circle, ring, check, cross, exclamation">
        <DemoRow className="gap-1">
          <Badge tone="warn">Pending</Badge>
          <Badge tone="approve">Approved</Badge>
          <Badge tone="deny">Denied</Badge>
          <Badge tone="neutral">Expired</Badge>
          <Badge tone="accent">Agent</Badge>
        </DemoRow>
      </Demo>

      <Demo label="Values" hint="fill, no icon: a bare value inside running text">
        <DemoRow className="gap-1">
          <Badge fill icon={false} className="font-mono">
            stdio
          </Badge>
          <Badge fill icon={false} className="font-mono">
            http
          </Badge>
          <Badge fill icon={false}>
            Built-in
          </Badge>
        </DemoRow>
      </Demo>

      <Demo
        label="Dense rows"
        hint="even in tight rows the state carries its word; state is color, never motion"
      >
        <div className="flex max-w-[420px] flex-col gap-1">
          {[
            { tone: "warn" as const, state: "Pending", label: "Awaiting review", meta: "39m" },
            { tone: "approve" as const, state: "Approved", label: "Approved by Ana", meta: "2h" },
            { tone: "deny" as const, state: "Denied", label: "Denied by Marcus", meta: "1d" },
            { tone: "neutral" as const, state: "Expired", label: "Expired unanswered", meta: "3d" },
          ].map((row) => (
            <div
              key={row.label}
              className="flex items-center gap-2.5 rounded-sm px-2 py-1.5 hover:bg-fill-hover"
            >
              <Badge tone={row.tone}>{row.state}</Badge>
              <span className="flex-1 text-body text-ink">{row.label}</span>
              <span className="font-mono text-micro font-normal text-ink-faint">
                {row.meta}
              </span>
            </div>
          ))}
        </div>
      </Demo>
    </Section>
  );
}
