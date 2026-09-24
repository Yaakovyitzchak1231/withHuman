import { CodeBlock } from "@/ds/components/code-block";
import { CopyButton } from "@/ds/components/copy-button";
import {
  DescriptionItem,
  DescriptionList,
} from "@/ds/components/description-list";
import { DiffView } from "@/ds/components/diff-view";
import { Disclosure } from "@/ds/components/disclosure";
import { HtmlPreview } from "@/ds/components/html-preview";
import { Section, Demo } from "../section";

const PAYLOAD = `{
  "charge": "ch_3Nq2XT2eZvKYlo2C",
  "amount": 42000,
  "reason": "requested_by_customer"
}`;

const BEFORE = `func Refund(amount int) error {
	if amount <= 0 {
		return errInvalid
	}
	return charge(-amount)
}`;

const AFTER = `func Refund(amount int) error {
	if amount <= 0 {
		return errInvalid
	}
	if amount > maxRefund {
		return errTooLarge
	}
	return charge(-amount)
}`;

const PATCH = `*** Begin Patch
*** Update File: config/limits.yaml
@@
 max_refund: 500
-rate_limit: 100
+rate_limit: 250
*** End Patch`;

const DRAFT = `<div>Hey René,</div><div><br></div><div>Thanks for the logs. Could you try a quick test with the AirPods disconnected?</div><div><br></div><div>Thanks,<br>Chris</div><img src="https://tracker.example.com/pixel.gif">`;

export function DetailBlocksSection() {
  return (
    <Section
      id="detail-blocks"
      title="Detail blocks"
      lede="The request-detail toolkit: copy affordances for ids and secrets, payload wells for machine content, and label-value pairs for facts."
    >
      <Demo label="CopyButton" hint="the icon confirms; ids show once">
        <div className="flex items-center gap-1.5">
          <span className="font-mono text-[12px] text-ink-secondary">
            req_9f2c81d740
          </span>
          <CopyButton value="req_9f2c81d740" aria-label="Copy request id" />
        </div>
      </Demo>

      <Demo label="CodeBlock" hint="hover reveals copy when there is no header">
        <div className="flex max-w-[440px] flex-col gap-3">
          <CodeBlock label="stripe.refunds.create" code={PAYLOAD} />
          <CodeBlock code={`withhuman enroll --token whk_3f9a1c77b2`} />
          <CodeBlock
            label="error"
            code="start ./Codex Computer Use.app/Contents/MacOS/SkyComputerUseClient: fork/exec: no such file or directory"
            wrap
            className="max-w-[320px]"
          />
        </div>
      </Demo>

      <Demo
        label="CodeBlock, raised"
        hint="on the canvas it takes the Card grammar instead of recessing"
        bare
      >
        <div className="max-w-[440px]">
          <CodeBlock label="Runtime context from the adapter" code={PAYLOAD} raised />
        </div>
      </Demo>

      <Demo label="DiffView" hint="a before and after lined up, or a patch as the tool sent it">
        <div className="flex max-w-[440px] flex-col gap-3">
          <DiffView label="Change" before={BEFORE} after={AFTER} />
          <DiffView patch={PATCH} />
        </div>
      </Demo>

      <Demo
        label="HtmlPreview"
        hint="sandboxed: no script runs, nothing loads, so the tracking pixel stays broken"
      >
        <div className="max-w-[440px]">
          <HtmlPreview html={DRAFT} label="html" />
        </div>
      </Demo>

      <Demo
        label="Disclosure"
        hint="an expandable separator; the action shows while open"
      >
        <div className="max-w-[440px]">
          <Disclosure
            label="Metadata"
            action={
              <CopyButton
                value={`{ "tool": "stripe.refunds.create" }`}
                aria-label="Copy metadata as JSON"
              />
            }
          >
            <DescriptionList>
              <DescriptionItem term="Tool" mono>
                stripe.refunds.create
              </DescriptionItem>
              <DescriptionItem term="Requested by">
                billing agent
              </DescriptionItem>
              <DescriptionItem term="Request id" mono>
                req_9f2c81d740
              </DescriptionItem>
            </DescriptionList>
          </Disclosure>
        </div>
      </Demo>

      <Demo
        label="DescriptionList"
        hint="string values copy by default; Expires opts out"
      >
        <div className="max-w-[360px]">
          <DescriptionList>
            <DescriptionItem term="Tool" mono>
              stripe.refunds.create
            </DescriptionItem>
            <DescriptionItem term="Amount" copy="420.00">
              $420.00
            </DescriptionItem>
            <DescriptionItem term="Requested by">
              billing agent
            </DescriptionItem>
            <DescriptionItem term="Request id" mono>
              req_9f2c81d740
            </DescriptionItem>
            <DescriptionItem term="Expires" copy={false}>
              in 23 hours
            </DescriptionItem>
          </DescriptionList>
        </div>
      </Demo>
    </Section>
  );
}
