package approvalpipeline

import (
	"fmt"
	"slices"

	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// A pipeline document is a tree: a branch holds its own blocks. Everything
// below the authoring surface works on the tree in pre-order, a branch
// before the blocks it holds, which is also the order of a revision's
// block_ids and the positions audit events and suspensions record.

// documentNode is one block of a document in pre-order. Parent is the
// index of the branch that holds it, -1 at the top; End is one past the
// last block inside it, which for anything but a branch is the next index.
type documentNode struct {
	Definition approvalblock.Definition
	Parent     int
	End        int
}

// flattenDocument lists a document's blocks in pre-order, refusing a
// document with more blocks than one pipeline may hold, nested ones
// included, before walking any further.
func flattenDocument(definitions []approvalblock.Definition) ([]documentNode, error) {
	nodes := make([]documentNode, 0, len(definitions))
	var walk func(blocks []approvalblock.Definition, parent int) error
	walk = func(blocks []approvalblock.Definition, parent int) error {
		for _, definition := range blocks {
			if len(nodes) == approvalblock.MaximumPipelineBlocks {
				return invalidPipelineDocument(fmt.Sprintf("pipeline must contain at most %d blocks, counting blocks inside branches", approvalblock.MaximumPipelineBlocks), nil)
			}
			index := len(nodes)
			nodes = append(nodes, documentNode{Definition: definition, Parent: parent, End: index + 1})
			if definition.Type == approvalblock.TypeBranch && definition.Branch != nil {
				if err := walk(definition.Branch.Blocks, index); err != nil {
					return err
				}
				nodes[index].End = len(nodes)
			}
		}
		return nil
	}
	if err := walk(definitions, -1); err != nil {
		return nil, err
	}
	return nodes, nil
}

// flattenDefinitions lists every block of a document in pre-order, for the
// checks that care what a document references rather than its shape.
func flattenDefinitions(definitions []approvalblock.Definition) []approvalblock.Definition {
	all := make([]approvalblock.Definition, 0, len(definitions))
	var walk func([]approvalblock.Definition)
	walk = func(blocks []approvalblock.Definition) {
		for _, definition := range blocks {
			all = append(all, definition)
			if definition.Type == approvalblock.TypeBranch && definition.Branch != nil {
				walk(definition.Branch.Blocks)
			}
		}
	}
	walk(definitions)
	return all
}

// definitionCondition returns the condition a block evaluates, if any.
func definitionCondition(definition approvalblock.Definition) (approvalblock.Condition, bool) {
	switch {
	case definition.CEL != nil:
		return definition.CEL.When, true
	case definition.Branch != nil:
		return definition.Branch.When, true
	case definition.Delegate != nil:
		return definition.Delegate.When, true
	default:
		return approvalblock.Condition{}, false
	}
}

// nestSnapshots rebuilds a revision's tree from its snapshots in pre-order.
// A stored branch names its direct blocks by key and they follow it; any
// other shape is refused, because a stored tree that does not read back
// exactly as it was saved must never execute.
func (registry *BlockRegistry) nestSnapshots(snapshots []domain.RequestApprovalBlock) ([]PipelineBlock, error) {
	next := 0
	var read func(keys []string) ([]PipelineBlock, error)
	readOne := func() (PipelineBlock, error) {
		snapshot := snapshots[next]
		next++
		definition, childKeys, err := registry.definitionFromSnapshot(snapshot)
		if err != nil {
			return PipelineBlock{}, err
		}
		block := PipelineBlock{SnapshotID: snapshot.ID, ConfigVersion: snapshot.ConfigVersion, ContentHash: snapshot.ContentHash, Definition: definition}
		if definition.Type == approvalblock.TypeBranch {
			children, err := read(childKeys)
			if err != nil {
				return PipelineBlock{}, err
			}
			block.Blocks = children
			block.Definition.Branch.Blocks = pipelineDefinitions(children)
		}
		return block, nil
	}
	read = func(keys []string) ([]PipelineBlock, error) {
		blocks := make([]PipelineBlock, 0, len(keys))
		for _, key := range keys {
			if next == len(snapshots) || snapshots[next].BlockKey != key {
				return nil, invalidPipelineDocument(fmt.Sprintf("stored branch block %q is not where its branch places it", key), nil)
			}
			block, err := readOne()
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
		return blocks, nil
	}
	top := make([]PipelineBlock, 0, len(snapshots))
	for next < len(snapshots) {
		block, err := readOne()
		if err != nil {
			return nil, err
		}
		top = append(top, block)
	}
	return top, nil
}

// pipelineDefinitions returns the authoring document of stored blocks; a
// branch's definition carries its blocks.
func pipelineDefinitions(blocks []PipelineBlock) []approvalblock.Definition {
	definitions := make([]approvalblock.Definition, len(blocks))
	for index, block := range blocks {
		definitions[index] = block.Definition
	}
	return definitions
}

type cursorStep uint8

const (
	stepBlock cursorStep = iota
	stepBranchEnd
	stepDone
)

// branchCursor walks one effective pipeline in pre-order. A branch or
// delegate whose condition matched is entered and its blocks (for a
// delegate, the target pipeline's) come next; one that did not is skipped
// whole. When an entered container runs out of blocks the cursor reports
// its end, which finishes the run: entering commits to it.
type branchCursor struct {
	blocks  []compiledBlock
	index   int
	entered []int
}

// newBranchCursor starts at start, inside every branch that holds it. A run
// reaches a position inside a branch only by entering the branch, so a
// resumed segment continues there without evaluating the conditions again.
func newBranchCursor(blocks []compiledBlock, start int) *branchCursor {
	cursor := &branchCursor{blocks: blocks, index: start}
	if start < len(blocks) {
		for parent := blocks[start].Parent; parent >= 0; parent = blocks[parent].Parent {
			cursor.entered = append(cursor.entered, parent)
		}
		slices.Reverse(cursor.entered)
	}
	return cursor
}

// next reports what comes next and at which index: the end of the innermost
// entered branch once its blocks are exhausted, else the block at the
// cursor, else the end of the section.
func (cursor *branchCursor) next() (cursorStep, int) {
	if depth := len(cursor.entered); depth > 0 {
		branch := cursor.entered[depth-1]
		if cursor.index >= cursor.blocks[branch].End {
			return stepBranchEnd, branch
		}
	}
	if cursor.index >= len(cursor.blocks) {
		return stepDone, -1
	}
	return stepBlock, cursor.index
}

// advance moves past a block that returned next.
func (cursor *branchCursor) advance() { cursor.index++ }

// enter moves into the branch at the cursor.
func (cursor *branchCursor) enter() {
	cursor.entered = append(cursor.entered, cursor.index)
	cursor.index++
}

// skip moves past the branch at the cursor and every block inside it.
func (cursor *branchCursor) skip() { cursor.index = cursor.blocks[cursor.index].End }
