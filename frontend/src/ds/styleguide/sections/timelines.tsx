import { Timeline, TimelineItem } from "@/ds/components/timeline";
import { Badge } from "@/ds/components/badge";
import { Avatar } from "@/ds/components/avatar";
import { AgentIcon, CheckIcon, ClockIcon, InfoIcon, PersonIcon, ShieldIcon } from "@/ds/icons";
import { Section, Demo } from "../section";

function Entry({
  title,
  at,
  detail,
}: {
  title: string;
  at: string;
  detail?: string;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-baseline justify-between gap-4">
        <span className="text-body text-ink">{title}</span>
        <span className="text-micro text-ink-faint tabular-nums">{at}</span>
      </div>
      {detail ? <p className="text-label font-normal text-ink-muted">{detail}</p> : null}
    </div>
  );
}

export function TimelinesSection() {
  return (
    <Section
      id="timelines"
      title="Timeline"
      lede="A vertical rail of ordered moments: escalation steps, routing evidence, notification history. Markers carry the tone; content stays ink."
    >
      <Demo label="Default markers" hint="the tone's status icon">
        <Timeline className="max-w-[420px]">
          <TimelineItem>
            <Entry title="Request created" at="02:14" />
          </TimelineItem>
          <TimelineItem>
            <Entry title="Pipeline returned human" at="02:14" />
          </TimelineItem>
          <TimelineItem tone="accent">
            <Entry title="Routed to payments path" at="02:14" />
          </TimelineItem>
        </Timeline>
      </Demo>

      <Demo label="References in running text" hint="markers follow the first text baseline, including wrapped lines">
        <Timeline className="max-w-[420px]">
          <TimelineItem tone="accent">
            <p className="text-body text-ink">
              Requested by <Badge inline icon={<AgentIcon width={13} height={13} />}>Release assistant</Badge>
            </p>
            <p className="text-micro text-ink-faint">14 Sept 2026, 11:57</p>
          </TimelineItem>
          <TimelineItem>
            <p className="text-body text-ink">
              Checked automatically · 1 rule ran, <Badge inline icon={<ShieldIcon width={13} height={13} />}>Production safety check</Badge> asked for a human
            </p>
            <p className="text-micro text-ink-faint">14 Sept 2026, 11:57</p>
          </TimelineItem>
          <TimelineItem>
            <p className="text-body text-ink">
              Claimed by <Badge inline icon={<Avatar name="Alex Reviewer" size="sm" tooltip={false} className="size-4 text-[8px]" />}>Alex Reviewer</Badge>
            </p>
            <p className="text-micro text-ink-faint">14 Sept 2026, 11:58</p>
          </TimelineItem>
          <TimelineItem tone="warn" marker={<ClockIcon width={14} height={14} />}>
            <p className="text-body text-ink">Waiting for a decision</p>
          </TimelineItem>
        </Timeline>
      </Demo>

      <Demo label="Nested timelines" hint="an item can hold a timeline; each rail ends at its own last item">
        <Timeline className="max-w-[420px]">
          <TimelineItem tone="accent" marker={<span className="font-mono text-[11px]">1</span>}>
            <Entry title="Staging never waits" at="Condition" />
          </TimelineItem>
          <TimelineItem tone="accent" marker={<span className="font-mono text-[11px]">2</span>}>
            <Entry title="Refunds" at="Branch" detail="Tool name equals stripe.refund" />
            <Timeline className="mt-3">
              <TimelineItem tone="accent" marker={<span className="font-mono text-[11px]">1</span>}>
                <Entry title="Fraud service review" at="Webhook" />
              </TimelineItem>
              <TimelineItem tone="accent" marker={<span className="font-mono text-[11px]">2</span>}>
                <Entry title="Approve small refunds" at="Condition" />
              </TimelineItem>
              <TimelineItem>
                <Entry title="No decision: ask a human" at="End" />
              </TimelineItem>
            </Timeline>
          </TimelineItem>
          <TimelineItem>
            <Entry title="No match: any authorized reviewer" at="End" />
          </TimelineItem>
        </Timeline>
        <p className="mt-3 max-w-[560px] text-micro text-ink-muted">
          The connector belongs to each item, so the outer rail runs past a nested timeline to its
          next item, and the nested rail stops at its own last item. The pipeline hover card nests a
          branch's blocks this way.
        </p>
      </Demo>

      <Demo label="Icon markers and tones" hint="an escalation walk">
        <Timeline className="max-w-[420px]">
          <TimelineItem marker={<ClockIcon width={14} height={14} />}>
            <Entry
              title="Deferred until 09:00"
              at="02:14"
              detail="Standard urgency waits for UK office hours."
            />
          </TimelineItem>
          <TimelineItem tone="accent" marker={<PersonIcon width={14} height={14} />}>
            <Entry
              title="Level 1 notified"
              at="09:00"
              detail="Payments approvers, Slack and queue."
            />
          </TimelineItem>
          <TimelineItem tone="warn" marker={<InfoIcon width={14} height={14} />}>
            <div className="flex flex-col gap-1">
              <Entry
                title="Level 2 notified"
                at="09:15"
                detail="Team lead paged; audience widened."
              />
              <div>
                <Badge tone="warn">Interrupt</Badge>
              </div>
            </div>
          </TimelineItem>
          <TimelineItem tone="approve" marker={<CheckIcon width={14} height={14} />}>
            <Entry title="Approved by the lead" at="09:27" />
          </TimelineItem>
        </Timeline>
      </Demo>
    </Section>
  );
}
