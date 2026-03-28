// Package optimism provides a BridgeAdapter stub for OP Mainnet (chain 10).
// OP Mainnet shares the OP Stack proof system with Base — the full implementation
// will be nearly identical to internal/bridge/base once the Base path is proven.
package optimism

import (
	"context"
	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

var _ usecase.BridgeAdapter = (*Adapter)(nil)

type Adapter struct{ portalAddress string }

func New(portalAddress string) *Adapter { return &Adapter{portalAddress: portalAddress} }

func (a *Adapter) Chain() entity.ChainID                                                       { return entity.ChainIDOptimism }
func (a *Adapter) ProveWithdrawal(_ context.Context, _ entity.BridgeEvent) (string, error)    { return "", nil }
func (a *Adapter) IsProven(_ context.Context, _ entity.BridgeEvent) (bool, error)             { return false, nil }
func (a *Adapter) IsFinalized(_ context.Context, _ entity.BridgeEvent) (bool, error)          { return false, nil }
func (a *Adapter) FinalizeWithdrawal(_ context.Context, _ entity.BridgeEvent) (string, error) { return "", nil }
func (a *Adapter) IsSettled(_ context.Context, _ entity.BridgeEvent) (bool, error)            { return false, nil }
