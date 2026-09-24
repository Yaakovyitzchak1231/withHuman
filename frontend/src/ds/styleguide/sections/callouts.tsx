import { Callout } from "@/ds/components/callout";
import { AlertIcon, CheckIcon } from "@/ds/icons";
import { Section, Demo } from "../section";

export function CalloutsSection() {
  return (
    <Section
      id="callouts"
      title="Callout"
      lede="Inline notices, the static sibling of Toast. The icon carries the tone; the text stays ink."
    >
      <Demo label="Tones" bare>
        <div className="flex max-w-[480px] flex-col gap-3">
          <Callout>
            Requests expire after 24 hours unless a policy extends them.
          </Callout>
          <Callout tone="accent" title="Two reviewers required">
            The payments policy needs a second approval before this posts.
          </Callout>
          <Callout tone="warn" icon={<AlertIcon width={15} height={15} />}>
            This action posts to Stripe immediately and cannot be undone.
          </Callout>
          <Callout tone="deny" icon={<AlertIcon width={15} height={15} />}>
            The agent's credential expired; new requests will be rejected.
          </Callout>
          <Callout tone="approve" icon={<CheckIcon width={15} height={15} />}>
            Connected. The agent can now submit requests to this queue.
          </Callout>
        </div>
      </Demo>
    </Section>
  );
}
