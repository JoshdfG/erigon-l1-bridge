package finality

import (
	"context"
	"fmt"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

var _ usecase.FinalityChecker = (*Checker)(nil)

type Checker struct {
	chain  usecase.ChainClient
	config entity.FinalityConfig
}

func New(chain usecase.ChainClient, config entity.FinalityConfig) *Checker {
	return &Checker{chain: chain, config: config}
}

func (c *Checker) Chain() entity.ChainID { return c.config.ChainID }

func (c *Checker) IsSafe(ctx context.Context, blockNumber uint64) (bool, error) {
	head, err := c.chain.BlockNumber(ctx)
	if err != nil { return false, fmt.Errorf("finality: %w", err) }
	return head >= blockNumber+c.config.SafeConfirmations, nil
}

func (c *Checker) IsFinalized(ctx context.Context, blockNumber uint64) (bool, error) {
	head, err := c.chain.BlockNumber(ctx)
	if err != nil { return false, fmt.Errorf("finality: %w", err) }
	return head >= blockNumber+c.config.FinalizedDelay, nil
}
