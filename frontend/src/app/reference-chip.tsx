import type { ReactNode } from "react";
import { Link, type LinkProps } from "@tanstack/react-router";
import { Avatar } from "@/ds/components/avatar";
import { Badge } from "@/ds/components/badge";
import { Referenced, type Reference } from "@/ds/components/reference";
import {
  AgentIcon,
  BranchIcon,
  BroadcastIcon,
  InboxIcon,
  MonitorIcon,
  PeopleIcon,
  ProgressIcon,
  ShieldIcon,
  ToolIcon,
} from "@/ds/icons";
import { cn } from "@/ds/lib/cn";
import { shortId } from "./format";

/*
 * A mention as a chip: the kind's icon, the name, and the reference
 * card on hover. Every mention of a thing outside a chip of its own
 * (a name in a sentence, a link in a list) renders through this, so
 * the icon is the reader's cue that there is a card to open. With a
 * link the chip is the way to the thing as well.
 */

function iconOf(reference: Reference, inline: boolean): ReactNode {
  switch (reference.kind) {
    case "person":
      return <Avatar name={reference.name} size="sm" tooltip={false} className={inline ? "size-4 text-[8px]" : undefined} />;
    case "team":
      return <PeopleIcon width={13} height={13} />;
    case "team_escalation_policy":
      return <BroadcastIcon width={13} height={13} />;
    case "escalation_path":
      return <ProgressIcon width={13} height={13} />;
    case "pipeline":
      return <BranchIcon width={13} height={13} />;
    case "webhook_endpoint":
      return <MonitorIcon width={13} height={13} />;
    case "agent":
      return <AgentIcon width={13} height={13} />;
    case "tool":
      return <ToolIcon width={13} height={13} />;
    case "pipeline_block":
      return <ShieldIcon width={13} height={13} />;
    case "request":
      return <InboxIcon width={13} height={13} />;
  }
}

function labelOf(reference: Reference): ReactNode {
  switch (reference.kind) {
    case "person":
    case "team":
    case "escalation_path":
    case "webhook_endpoint":
    case "agent":
      return reference.name;
    case "pipeline_block":
      return reference.name || reference.key;
    case "team_escalation_policy":
      return `${reference.teamName} policy`;
    case "pipeline":
      return reference.name ?? <span className="font-mono text-[12px]">{reference.pipelineKey}</span>;
    case "tool":
      return <span className="font-mono text-[12px]">{reference.name}</span>;
    case "request":
      return <span className="font-mono text-[12px]">{shortId(reference.id)}</span>;
  }
}

export function ReferenceChip({
  reference,
  inline = false,
  link,
  children,
  className,
}: {
  reference: Reference;
  /* Use the sentence's baseline and compact spacing for prose mentions. */
  inline?: boolean;
  /* Where the chip goes on click; without one it only opens its card. */
  link?: Pick<LinkProps, "to" | "params" | "search">;
  /* Overrides the name as the chip's label. */
  children?: ReactNode;
  className?: string;
}) {
  const chip = (
    <Badge
      inline={inline}
      /* A face sits in the pill's curve; an icon keeps the pill's inset. */
      className={cn(!inline && "gap-1", !inline && reference.kind === "person" && "pl-0.5", className)}
      icon={iconOf(reference, inline)}
    >
      {children ?? labelOf(reference)}
    </Badge>
  );
  return (
    <Referenced
      reference={reference}
      inline={inline}
      focusable={!link}
      triggerClassName={inline ? "-mx-0.5 px-0.5" : undefined}
    >
      {link ? (
        <Link {...link} className={inline ? "inline rounded-sm" : "inline-flex rounded-full"} onClick={(event) => event.stopPropagation()}>
          {chip}
        </Link>
      ) : (
        chip
      )}
    </Referenced>
  );
}
