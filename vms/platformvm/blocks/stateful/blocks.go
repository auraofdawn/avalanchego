// Copyright (C) 2019-2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package stateful

import (
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/snow/choices"
	"github.com/ava-labs/avalanchego/snow/consensus/snowman"
	"github.com/ava-labs/avalanchego/vms/platformvm/blocks/stateless"
)

var (
	_ snowman.Block       = &Block{}
	_ snowman.OracleBlock = &OracleBlock{}
)

// Exported for testing in platformvm package.
type Block struct {
	stateless.Block
	manager *manager
}

func (b *Block) Verify() error {
	return b.Visit(b.manager.verifier)
}

func (b *Block) Accept() error {
	return b.Visit(b.manager.acceptor)
}

func (b *Block) Reject() error {
	return b.Visit(b.manager.rejector)
}

func (b *Block) Status() choices.Status {
	blkID := b.ID()

	if b.manager.backend.lastAccepted == blkID {
		return choices.Accepted
	}
	// Check if the block is in memory. If so, it's processing.
	if _, ok := b.manager.backend.blkIDToState[blkID]; ok {
		return choices.Processing
	}
	// Block isn't in memory. Check in the database.
	_, status, err := b.manager.state.GetStatelessBlock(blkID)
	if err != nil {
		// It isn't in the database.
		return choices.Processing
	}
	return status
}

func (b *Block) Timestamp() time.Time {
	// If this is the last accepted block and the block was loaded from disk
	// since it was accepted, then the timestamp wouldn't be set correctly. So,
	// we explicitly return the chain time.
	// Check if the block is processing.
	if blkState, ok := b.manager.blkIDToState[b.ID()]; ok {
		return blkState.timestamp
	}
	// The block isn't processing.
	// According to the snowman.Block interface, the last accepted
	// block is the only accepted block that must return a correct timestamp,
	// so we just return the chain time.
	return b.manager.state.GetTimestamp()
}

// Exported for testing in platformvm package.
type OracleBlock struct {
	// Invariant: The inner statless block is a *stateless.ProposalBlock.
	*Block
}

func (b *OracleBlock) Options() ([2]snowman.Block, error) {
	blkID := b.ID()
	nextHeight := b.Height() + 1

	statelessCommitBlk, err := stateless.NewCommitBlock(
		blkID,
		nextHeight,
	)
	if err != nil {
		return [2]snowman.Block{}, fmt.Errorf(
			"failed to create commit block: %w",
			err,
		)
	}
	commitBlock := b.manager.NewBlock(statelessCommitBlk)

	statelessAbortBlk, err := stateless.NewAbortBlock(
		blkID,
		nextHeight,
	)
	if err != nil {
		return [2]snowman.Block{}, fmt.Errorf(
			"failed to create abort block: %w",
			err,
		)
	}
	abortBlock := b.manager.NewBlock(statelessAbortBlk)

	blkState, ok := b.manager.backend.blkIDToState[blkID]
	if !ok {
		return [2]snowman.Block{}, fmt.Errorf("block %s state not found", blkID)
	}
	if blkState.inititallyPreferCommit {
		return [2]snowman.Block{commitBlock, abortBlock}, nil
	}
	return [2]snowman.Block{abortBlock, commitBlock}, nil
}