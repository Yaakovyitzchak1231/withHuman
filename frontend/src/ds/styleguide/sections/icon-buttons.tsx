import { IconButton } from "@/ds/components/icon-button";
import {
  CopyIcon,
  EllipsisIcon,
  PlusIcon,
  TrashIcon,
  XIcon,
} from "@/ds/icons";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function IconButtonsSection() {
  return (
    <Section
      id="icon-buttons"
      title="IconButton"
      lede="Square, quiet, and always labeled for assistive tech. Icons are hand-drawn 16px strokes; no icon library."
    >
      <Demo label="Variants and sizes">
        <DemoRow className="gap-4">
          <IconButton aria-label="More actions">
            <EllipsisIcon />
          </IconButton>
          <IconButton aria-label="Copy id">
            <CopyIcon />
          </IconButton>
          <IconButton aria-label="New request" variant="neutral">
            <PlusIcon />
          </IconButton>
          <IconButton aria-label="Close" variant="neutral" size="sm">
            <XIcon width={14} height={14} />
          </IconButton>
          <IconButton aria-label="Delete" className="hover:text-deny">
            <TrashIcon />
          </IconButton>
          <IconButton aria-label="Disabled action" disabled>
            <CopyIcon />
          </IconButton>
        </DemoRow>
        <KeyboardHint>
          Tab focuses, Enter or Space activates. Labels surface through
          tooltips once wired.
        </KeyboardHint>
      </Demo>
    </Section>
  );
}
