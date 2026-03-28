// Package arbitrum provides a BridgeAdapter stub for Arbitrum One (chain 42161).
// Arbitrum uses a different fraud proof model — implement after the OP Stack path works.
package arbitrum

import (
	"context"
	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

var _ usecase.BridgeAdapter = (*Adapter)(nil)

type Adapter struct{ rollupAddress string }

func New(rollupAddress string) *Adapter { return &Adapter{rollupAddress: rollupAddress} }

func (a *Adapter) Chain() entity.ChainID                                                       { return entity.ChainIDArbitrum }
func (a *Adapter) ProveWithdrawal(_ context.Context, _ entity.BridgeEvent) (string, error)    { return "", nil }
func (a *Adapter) IsProven(_ context.Context, _ entity.BridgeEvent) (bool, error)             { return false, nil }
func (a *Adapter) IsFinalized(_ context.Context, _ entity.BridgeEvent) (bool, error)          { return false, nil }
func (a *Adapter) FinalizeWithdrawal(_ context.Context, _ entity.BridgeEvent) (string, error) { return "", nil }
func (a *Adapter) IsSettled(_ context.Context, _ entity.BridgeEvent) (bool, error)            { return false, nil }
