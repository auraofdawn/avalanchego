// Copyright (C) 2019-2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package blocks

import (
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/ids"

	transactions "github.com/ava-labs/avalanchego/vms/platformvm/txs"
)

var (
	_ Block = &BlueberryStandardBlock{}
	_ Block = &ApricotStandardBlock{}
)

func NewBlueberryStandardBlock(timestamp time.Time, parentID ids.ID, height uint64, txs []*transactions.Tx) (Block, error) {
	res := &BlueberryStandardBlock{
		BlueberryCommonBlock: BlueberryCommonBlock{
			ApricotCommonBlock: ApricotCommonBlock{
				PrntID: parentID,
				Hght:   height,
			},
			BlkTimestamp: uint64(timestamp.Unix()),
		},
		Transactions: txs,
	}

	return res, initialize(Block(res))
}

type BlueberryStandardBlock struct {
	BlueberryCommonBlock `serialize:"true"`

	Transactions []*transactions.Tx `serialize:"true" json:"txs"`
}

func (b *BlueberryStandardBlock) initialize(bytes []byte) error {
	b.BlueberryCommonBlock.initialize(bytes)
	for _, tx := range b.Transactions {
		if err := tx.Sign(transactions.Codec, nil); err != nil {
			return fmt.Errorf("failed to initialize tx: %w", err)
		}
	}
	return nil
}

func (b *BlueberryStandardBlock) Txs() []*transactions.Tx { return b.Transactions }

func (b *BlueberryStandardBlock) Visit(v Visitor) error {
	return v.BlueberryStandardBlock(b)
}

func NewApricotStandardBlock(parentID ids.ID, height uint64, txs []*transactions.Tx) (Block, error) {
	res := &ApricotStandardBlock{
		ApricotCommonBlock: ApricotCommonBlock{
			PrntID: parentID,
			Hght:   height,
		},
		Transactions: txs,
	}

	return res, initialize(Block(res))
}

type ApricotStandardBlock struct {
	ApricotCommonBlock `serialize:"true"`

	Transactions []*transactions.Tx `serialize:"true" json:"txs"`
}

func (b *ApricotStandardBlock) initialize(bytes []byte) error {
	b.ApricotCommonBlock.initialize(bytes)
	for _, tx := range b.Transactions {
		if err := tx.Sign(transactions.Codec, nil); err != nil {
			return fmt.Errorf("failed to sign block: %w", err)
		}
	}
	return nil
}

func (b *ApricotStandardBlock) Txs() []*transactions.Tx { return b.Transactions }

func (b *ApricotStandardBlock) Visit(v Visitor) error {
	return v.ApricotStandardBlock(b)
}
