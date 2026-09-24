import { IconButton } from "@/ds/components/icon-button";
import { Button } from "@/ds/components/button";
import { Tooltip, TooltipProvider } from "@/ds/components/tooltip";
import { CopyIcon, EllipsisIcon, TrashIcon } from "@/ds/icons";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function TooltipsSection() {
  return (
    <Section
      id="tooltips"
      title="Tooltip"
      lede="500ms warm-up, then instant between neighbors. Inverse surface in both themes; shortcuts ride along as inverse keycaps."
    >
      <Demo label="On icon buttons" hint="hover or focus to open">
        <TooltipProvider>
          <DemoRow className="gap-3">
            <Tooltip content="Copy request id" keys="mod+C">
              <IconButton aria-label="Copy request id">
                <CopyIcon />
              </IconButton>
            </Tooltip>
            <Tooltip content="More actions">
              <IconButton aria-label="More actions">
                <EllipsisIcon />
              </IconButton>
            </Tooltip>
            <Tooltip content="Delete draft" side="bottom">
              <IconButton aria-label="Delete draft" className="hover:text-deny">
                <TrashIcon />
              </IconButton>
            </Tooltip>
            <Tooltip content="Approve request" keys="A">
              <Button variant="approve" size="sm">
                Approve
              </Button>
            </Tooltip>
          </DemoRow>
        </TooltipProvider>
        <KeyboardHint>
          Focusing a trigger with Tab opens its tooltip; Esc dismisses.
        </KeyboardHint>
      </Demo>
    </Section>
  );
}
