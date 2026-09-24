import { useState } from "react";
import {
  CompoundSelect,
  CompoundSelectInput,
  CompoundSelectTrigger,
} from "@/ds/components/compound-select";
import { Select, SelectContent, SelectItem } from "@/ds/components/select";
import { Section, Demo, KeyboardHint } from "../section";

/* The audit log's subject vocabulary: the product's nouns. */
const SUBJECT_TYPES = [
  "approval_request",
  "agent",
  "agent_instance",
  "membership",
  "invitation",
  "team",
  "request_approval_pipeline",
  "escalation_path",
  "webhook_endpoint",
  "tool_presentation",
  "organization",
];

const mono = (item: string) => <span className="font-mono text-[12px]">{item}</span>;

interface Kind {
  label: string;
  value: string;
  names: string[];
}

const KINDS: Kind[] = [
  { label: "Member", value: "member", names: ["Ada Lovelace", "Grace Hopper", "Edsger Dijkstra"] },
  { label: "Team", value: "team", names: ["Billing", "Platform", "Support"] },
  { label: "Agent", value: "agent", names: ["billing-agent-01", "deploy-bot", "crm-sync"] },
];

function SubjectFilter({
  type,
  onTypeChange,
  id,
  onIdChange,
  disabled,
}: {
  type: string | null;
  onTypeChange: (type: string | null) => void;
  id: string;
  onIdChange: (id: string) => void;
  disabled?: boolean;
}) {
  return (
    <CompoundSelect
      aria-label="Subject type"
      items={SUBJECT_TYPES}
      renderItem={mono}
      emptyLabel="Any subject"
      value={type}
      onValueChange={(next) => {
        onTypeChange(next);
        if (next === null) onIdChange("");
      }}
      disabled={disabled}
    >
      <CompoundSelectInput
        aria-label="Subject id"
        placeholder="Any id"
        value={id}
        onChange={(event) => onIdChange(event.target.value)}
      />
    </CompoundSelect>
  );
}

export function CompoundSelectSection() {
  const [type, setType] = useState<string | null>(null);
  const [id, setId] = useState("");
  const [pickedType, setPickedType] = useState<string | null>("approval_request");
  const [pickedId, setPickedId] = useState("req_01j9x4k2m7");
  const [kind, setKind] = useState<Kind | null>(null);
  const [name, setName] = useState<string | null>(null);

  const hint = type ? `subject=${type}${id ? ` subject_id=${id}` : ""}` : "no subject filter";
  const reviewerHint =
    kind && name ? `${kind.value}:${name}` : kind ? `${kind.value}, nobody yet` : "nothing chosen";

  return (
    <Section
      id="compound-select"
      title="CompoundSelect"
      lede="A Select whose chosen value unlocks a second control in the same box. Choosing a value slides it left into a prefix segment and the tail, an input or another Select, opens beside it."
    >
      <Demo label="Audit subject filter" hint={hint}>
        <div className="flex max-w-[360px] flex-col gap-1.5">
          <span className="text-label text-ink-secondary">Subject</span>
          <SubjectFilter type={type} onTypeChange={setType} id={id} onIdChange={setId} />
        </div>
        <KeyboardHint>
          Choosing a value moves focus into the tail; Tab moves between the
          segments. Choosing "Any subject" collapses the box back to a plain
          Select and drops the id with it.
        </KeyboardHint>
      </Demo>

      <Demo label="Select tail" hint={reviewerHint}>
        <div className="flex max-w-[360px] flex-col gap-1.5">
          <span className="text-label text-ink-secondary">Reviewer</span>
          <CompoundSelect
            aria-label="Reviewer kind"
            items={KINDS}
            itemKey={(item) => item.value}
            itemLabel={(item) => item.label}
            placeholder="Choose a kind"
            value={kind}
            onValueChange={(next) => {
              setKind(next);
              setName(null);
            }}
          >
            {kind ? (
              <Select
                items={kind.names}
                itemToStringLabel={(item) => item as string}
                value={name}
                onValueChange={(next) => setName(next as string | null)}
              >
                <CompoundSelectTrigger aria-label={`${kind.label} name`} segment="tail">
                  {name ?? (
                    <span className="text-ink-faint">Choose a {kind.label.toLowerCase()}</span>
                  )}
                </CompoundSelectTrigger>
                <SelectContent<string>>
                  {(item) => (
                    <SelectItem key={item} value={item}>
                      {item}
                    </SelectItem>
                  )}
                </SelectContent>
              </Select>
            ) : null}
          </CompoundSelect>
        </div>
        <KeyboardHint>
          A placeholder instead of an empty label: here nothing chosen is a
          prompt, not a value. The tail is a plain Select wearing the
          borderless trigger.
        </KeyboardHint>
      </Demo>

      <Demo
        label="States"
        hint="both widths the audit rail's grid can give it: one 180px cell and two"
      >
        <div className="flex flex-col gap-5">
          <div className="flex flex-col gap-1.5">
            <span className="text-micro text-ink-faint">Value chosen, id typed, one cell</span>
            <div className="w-[180px]">
              <SubjectFilter
                type={pickedType}
                onTypeChange={setPickedType}
                id={pickedId}
                onIdChange={setPickedId}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-micro text-ink-faint">Same value, two cells</span>
            <div className="w-[372px]">
              <SubjectFilter
                type={pickedType}
                onTypeChange={setPickedType}
                id={pickedId}
                onIdChange={setPickedId}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-micro text-ink-faint">Long value, no id yet</span>
            <div className="w-[180px]">
              <SubjectFilter
                type="request_approval_pipeline"
                onTypeChange={() => {}}
                id=""
                onIdChange={() => {}}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-micro text-ink-faint">Disabled</span>
            <div className="w-[180px]">
              <SubjectFilter
                type="agent"
                onTypeChange={() => {}}
                id="agt_7c1"
                onIdChange={() => {}}
                disabled
              />
            </div>
          </div>
        </div>
      </Demo>
    </Section>
  );
}
