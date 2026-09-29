import { Badge } from "@/ds/components/badge";
import type { PipelineUse } from "../types";
import { pipelineLink } from "./pipeline-link";
import { ReferenceChip } from "./reference-chip";

/* The pipeline a use belongs to, as a chip that opens its card and
 * links to its page. The chip shows the key; the card names it. */
export function PipelineUseLink({ use }: { use: PipelineUse }) {
  return (
    <ReferenceChip
      reference={{ kind: "pipeline", pipelineKey: use.pipeline_key }}
      link={pipelineLink(use.pipeline_key)}
    />
  );
}

/* The active pipeline revisions that reference a shared object, one row
 * per pipeline revision. Which block names it (or whether it is the
 * revision's default) is the pipeline page's detail, not the reader's:
 * a revision naming the object twice is still one revision to replace. */
export function PipelineUseList({ uses }: { uses: PipelineUse[] }) {
  const revisions = uses.filter(
    (use, index) => uses.findIndex((other) => other.pipeline_id === use.pipeline_id) === index,
  );
  return (
    <ul className="flex flex-col gap-1">
      {revisions.map((use) => (
        <li key={use.pipeline_id} className="flex items-center gap-2 text-body text-ink-secondary">
          <Badge tone="accent">Pipeline</Badge>
          <PipelineUseLink use={use} />
          <span>revision {use.revision}</span>
        </li>
      ))}
    </ul>
  );
}
