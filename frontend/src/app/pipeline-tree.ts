import type { ConditionNode } from "@/ds/components/condition-builder";
import type { BlockDraft, BranchDraft } from "./pipeline-blocks";

/*
 * The draft pipeline as a tree: a rail of blocks, where a branch holds a
 * rail of its own, to any depth. Every structural edit the editor makes
 * goes through these pure helpers, addressed by block id (block keys are
 * unique across the whole tree), and returns a new tree that shares every
 * rail it did not touch.
 */

/* One block met on a pre-order walk: a branch, then everything inside
 * it, then the next block. */
export interface DraftVisit {
  block: BlockDraft;
  /* 1-based positions from the top rail down: [3, 2] is the second
   * block inside the third. */
  path: number[];
  /* The branches enclosing the block, outermost first. */
  ancestors: BranchDraft[];
  /* The enclosing branch's id, or null on the top rail. */
  parentId: string | null;
  /* 0-based index within its own rail. */
  index: number;
}

export function walkDrafts(drafts: BlockDraft[]): DraftVisit[] {
  const visits: DraftVisit[] = [];
  const walk = (rail: BlockDraft[], path: number[], ancestors: BranchDraft[]) => {
    rail.forEach((block, index) => {
      const here = [...path, index + 1];
      visits.push({ block, path: here, ancestors, parentId: ancestors.at(-1)?.id ?? null, index });
      if (block.kind === "branch") walk(block.blocks, here, [...ancestors, block]);
    });
  };
  walk(drafts, [], []);
  return visits;
}

export function findDraft(drafts: BlockDraft[], id: string): DraftVisit | undefined {
  return walkDrafts(drafts).find((visit) => visit.block.id === id);
}

/* Every block in the tree, the ones inside branches included. */
export function countDrafts(drafts: BlockDraft[]): number {
  return walkDrafts(drafts).length;
}

/* The chip a block carries: "3", or "3.2.1" inside branches. */
export function positionLabel(path: number[]): string {
  return path.join(".");
}

/* A rail's blocks: the top rail for null, else the named branch's. */
export function railOf(drafts: BlockDraft[], parentId: string | null): BlockDraft[] {
  if (parentId === null) return drafts;
  const parent = findDraft(drafts, parentId)?.block;
  return parent?.kind === "branch" ? parent.blocks : [];
}

/* Replaces one rail's blocks. An unchanged rail returns the tree as is. */
function mapRail(
  drafts: BlockDraft[],
  parentId: string | null,
  change: (rail: BlockDraft[]) => BlockDraft[],
): BlockDraft[] {
  if (parentId === null) return change(drafts);
  const parent = findDraft(drafts, parentId)?.block;
  if (parent?.kind !== "branch") return drafts;
  const blocks = change(parent.blocks);
  if (blocks === parent.blocks) return drafts;
  return updateDraft(drafts, parentId, (block) => (block.kind === "branch" ? { ...block, blocks } : block));
}

/* The block with this id, replaced by what change makes of it, at any
 * depth. */
export function updateDraft(
  drafts: BlockDraft[],
  id: string,
  change: (block: BlockDraft) => BlockDraft,
): BlockDraft[] {
  let changed = false;
  const next = drafts.map((block) => {
    if (block.id === id) {
      changed = true;
      return change(block);
    }
    if (block.kind === "branch") {
      const blocks = updateDraft(block.blocks, id, change);
      if (blocks !== block.blocks) {
        changed = true;
        return { ...block, blocks };
      }
    }
    return block;
  });
  return changed ? next : drafts;
}

/* The tree without the block with this id, and everything inside it. */
export function removeDraft(drafts: BlockDraft[], id: string): BlockDraft[] {
  let changed = false;
  const next: BlockDraft[] = [];
  for (const block of drafts) {
    if (block.id === id) {
      changed = true;
      continue;
    }
    if (block.kind === "branch") {
      const blocks = removeDraft(block.blocks, id);
      if (blocks !== block.blocks) {
        changed = true;
        next.push({ ...block, blocks });
        continue;
      }
    }
    next.push(block);
  }
  return changed ? next : drafts;
}

/* block, placed right after the block with afterId, in the same rail. */
export function insertAfter(drafts: BlockDraft[], afterId: string, block: BlockDraft): BlockDraft[] {
  const visit = findDraft(drafts, afterId);
  if (!visit) return drafts;
  return mapRail(drafts, visit.parentId, (rail) => [
    ...rail.slice(0, visit.index + 1),
    block,
    ...rail.slice(visit.index + 1),
  ]);
}

/* block, added at the end of a rail: the top rail for null, else the
 * named branch's. */
export function appendToRail(drafts: BlockDraft[], parentId: string | null, block: BlockDraft): BlockDraft[] {
  return mapRail(drafts, parentId, (rail) => [...rail, block]);
}

/* Moves the block at index from to index to, within one rail. Drag and
 * Move up or down never cross a branch's edge. */
export function reorderRail(
  drafts: BlockDraft[],
  parentId: string | null,
  from: number,
  to: number,
): BlockDraft[] {
  return mapRail(drafts, parentId, (rail) => {
    if (from === to || from < 0 || to < 0 || from >= rail.length || to >= rail.length) return rail;
    const next = [...rail];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    return next;
  });
}

/* The block right after a branch joins that branch as its last block.
 * Anything else stays where it is. */
export function moveIntoBranchAbove(drafts: BlockDraft[], id: string): BlockDraft[] {
  const visit = findDraft(drafts, id);
  if (!visit || visit.index === 0) return drafts;
  const above = railOf(drafts, visit.parentId)[visit.index - 1];
  if (above?.kind !== "branch") return drafts;
  const without = mapRail(drafts, visit.parentId, (rail) => rail.filter((_, index) => index !== visit.index));
  return appendToRail(without, above.id, visit.block);
}

/* A block inside a branch leaves it and becomes the block right after
 * it. A top-level block stays where it is. */
export function moveOutOfBranch(drafts: BlockDraft[], id: string): BlockDraft[] {
  const visit = findDraft(drafts, id);
  const parent = visit?.ancestors.at(-1);
  if (!visit || !parent) return drafts;
  return insertAfter(removeDraft(drafts, id), parent.id, visit.block);
}

/* A branch replaced by its own blocks, where it stood: the condition,
 * reason and path go, the blocks stay. */
export function unwrapBranch(drafts: BlockDraft[], id: string): BlockDraft[] {
  const visit = findDraft(drafts, id);
  if (!visit || visit.block.kind !== "branch") return drafts;
  const { blocks } = visit.block;
  return mapRail(drafts, visit.parentId, (rail) => [
    ...rail.slice(0, visit.index),
    ...blocks,
    ...rail.slice(visit.index + 1),
  ]);
}

/* Every condition the tree tests: each Conditions block's, each
 * branch's and each delegate's, at any depth. */
export function allConditions(drafts: BlockDraft[]): ConditionNode[] {
  return walkDrafts(drafts).flatMap(({ block }) =>
    block.kind === "cel" || block.kind === "branch" || block.kind === "delegate" ? [block.when] : [],
  );
}

/* How many blocks the draft changed against the baseline: each block
 * added, removed, edited or moved to another rail counts once, and a
 * branch counts for its own fields, not its blocks'. A pure reorder
 * counts as one change. */
export function changedBlockCount(baseline: BlockDraft[], draft: BlockDraft[]): number {
  const signature = ({ block, parentId }: DraftVisit) =>
    JSON.stringify({ parentId, block: block.kind === "branch" ? { ...block, blocks: undefined } : block });
  const saved = new Map(walkDrafts(baseline).map((visit) => [visit.block.id, signature(visit)]));
  const current = walkDrafts(draft);
  let count = 0;
  for (const visit of current) {
    const before = saved.get(visit.block.id);
    if (before === undefined || before !== signature(visit)) count += 1;
    saved.delete(visit.block.id);
  }
  count += saved.size;
  const order = (visits: DraftVisit[]) => visits.map((visit) => visit.block.id).join(",");
  if (count === 0 && order(walkDrafts(baseline)) !== order(current)) count = 1;
  return count;
}

/* Which blocks no request can reach. An enabled Always matches every
 * request that gets to it, so the blocks after it on its rail never
 * run; inside a branch that also leaves the branch's end unused. Only
 * an Always on the pipeline's own rail stops the pipeline: one inside a
 * branch stops only that branch, because requests that skip the branch
 * go on. An unreachable branch passes its blocker to everything inside
 * it. A delegate is a leaf here: what its target does is the target's
 * own business, and requests that skip it go on. Blocks are keyed by
 * id, which is unique within a pipeline; a delegated pipeline's
 * reachability is computed on its own. */
export interface Reachability {
  /* The Always that makes each unreachable block unreachable. */
  blockedBy: Map<string, BlockDraft>;
  /* Branches whose end no request reaches, with the Always responsible. */
  endBlockedBy: Map<string, BlockDraft>;
  /* The Always on the pipeline's own rail that ends it. */
  stoppedBy?: BlockDraft;
}

export function pipelineReachability(drafts: BlockDraft[]): Reachability {
  const blockedBy = new Map<string, BlockDraft>();
  const endBlockedBy = new Map<string, BlockDraft>();
  const walkRail = (rail: BlockDraft[], inherited: BlockDraft | undefined): BlockDraft | undefined => {
    let stop = inherited;
    for (const block of rail) {
      if (stop) blockedBy.set(block.id, stop);
      if (block.kind === "branch") {
        const end = walkRail(block.blocks, stop);
        if (end) endBlockedBy.set(block.id, end);
      }
      if (!stop && block.kind === "always" && block.enabled) stop = block;
    }
    return stop;
  };
  const stoppedBy = walkRail(drafts, undefined);
  return { blockedBy, endBlockedBy, stoppedBy };
}
