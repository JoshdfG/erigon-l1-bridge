// Package base implements ChainClient for Base (OP Stack, chain ID 8453).
//
// Connectivity model:
//   - RPC (HTTP/HTTPS): used for FilterLogs polling and one-off calls
//   - WS (optional):    used for real-time eth_subscribe when WSURL is set
//
// The client falls back to polling when no WebSocket URL is configured.
// Polling interval is set by the caller; Base produces a block every ~2s.
package base

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

// compile-time interface check
var _ usecase.ChainClient = (*Client)(nil)

// Client wraps go-ethereum's ethclient for Base.
type Client struct {
	ec           *ethclient.Client  // HTTP client — always present
	ws           *ethclient.Client  // WebSocket client — nil if no WSURL
	chainID      entity.ChainID
	pollInterval time.Duration
}

// Config holds connection parameters for the Base client.
type Config struct {
	RPCURL       string        // HTTP/HTTPS RPC endpoint (required)
	WSURL        string        // ws/wss endpoint (optional; enables subscriptions)
	PollInterval time.Duration // fallback poll interval when WS unavailable
}

// New dials the Base RPC endpoint.
// The WebSocket connection is best-effort — if WSURL is empty or the dial
// fails, the client falls back to polling via FilterLogs.
func New(cfg Config) (*Client, error) {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}

	ec, err := ethclient.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("base client: dial RPC %s: %w", cfg.RPCURL, err)
	}

	c := &Client{
		ec:           ec,
		chainID:      entity.ChainIDBase,
		pollInterval: cfg.PollInterval,
	}

	if cfg.WSURL != "" {
		ws, err := ethclient.Dial(cfg.WSURL)
		if err != nil {
			// Non-fatal: log the warning and fall back to polling
			log.Printf("base client: WS dial failed (%v); using poll fallback", err)
		} else {
			c.ws = ws
		}
	}

	return c, nil
}

// NewFromURL is a convenience constructor for the common case of HTTP-only.
func NewFromURL(rpcURL string) (*Client, error) {
	return New(Config{RPCURL: rpcURL})
}

func (c *Client) ChainID() entity.ChainID { return c.chainID }

func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	n, err := c.ec.BlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("base: block number: %w", err)
	}
	return n, nil
}

// SubscribeLogs watches for OP Stack bridge events on Base and calls handler
// for each decoded BridgeEvent.
//
// Strategy:
//  1. If a WebSocket client is available, use eth_subscribe (push).
//  2. Otherwise, poll with eth_getLogs on a timer.
//
// The function blocks until ctx is cancelled.
func (c *Client) SubscribeLogs(ctx context.Context, handler func(entity.BridgeEvent)) error {
	filterQuery := c.bridgeFilterQuery(nil, nil)

	if c.ws != nil {
		return c.subscribeWS(ctx, filterQuery, handler)
	}
	return c.pollLogs(ctx, filterQuery, handler)
}

// bridgeFilterQuery builds the ethereum.FilterQuery for all OP Stack bridge events.
// fromBlock / toBlock are nil for live subscriptions (uses "latest").
func (c *Client) bridgeFilterQuery(fromBlock, toBlock *big.Int) ethereum.FilterQuery {
	return ethereum.FilterQuery{
		FromBlock: fromBlock,
		ToBlock:   toBlock,
		Addresses: []common.Address{
			common.HexToAddress(L2StandardBridgeAddr),
			common.HexToAddress(L2ToL1MessagePasserAddr),
		},
		Topics: [][]common.Hash{
			// topic[0]: match any of our three event signatures
			{
				topicETHBridgeInitiated,
				topicERC20BridgeInitiated,
				topicMessagePassed,
			},
		},
	}
}

// subscribeWS uses eth_subscribe (WebSocket) for real-time log delivery.
func (c *Client) subscribeWS(ctx context.Context, query ethereum.FilterQuery, handler func(entity.BridgeEvent)) error {
	logCh := make(chan types.Log, 64)

	sub, err := c.ws.SubscribeFilterLogs(ctx, query, logCh)
	if err != nil {
		return fmt.Errorf("base: ws subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	log.Printf("base: WS subscription active on %s + %s", L2StandardBridgeAddr, L2ToL1MessagePasserAddr)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-sub.Err():
			return fmt.Errorf("base: ws subscription error: %w", err)

		case l := <-logCh:
			if l.Removed {
				// Reorg — the event is being un-applied; downstream finality
				// checker will naturally deal with this, but log it clearly.
				log.Printf("base: reorg detected, log removed tx=%s idx=%d", l.TxHash.Hex(), l.Index)
				continue
			}
			event, ok, err := parseLog(l)
			if err != nil {
				log.Printf("base: parse log error tx=%s: %v", l.TxHash.Hex(), err)
				continue
			}
			if !ok {
				continue
			}
			handler(event)
		}
	}
}

// pollLogs uses eth_getLogs on a timer — the fallback when WebSocket is unavailable.
// It tracks the last processed block to avoid re-delivering events on each tick.
func (c *Client) pollLogs(ctx context.Context, query ethereum.FilterQuery, handler func(entity.BridgeEvent)) error {
	// Start polling from the current head; don't replay history on startup.
	head, err := c.ec.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("base: poll: initial block number: %w", err)
	}
	fromBlock := head

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	log.Printf("base: polling logs every %s from block %d", c.pollInterval, fromBlock)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			toBlock, err := c.ec.BlockNumber(ctx)
			if err != nil {
				log.Printf("base: poll: block number error: %v", err)
				continue
			}
			if toBlock < fromBlock {
				// Head moved backward — chain reorg at the tip.  Safe to wait.
				continue
			}
			if toBlock == fromBlock-1 {
				// No new blocks yet.
				continue
			}

			rangeQuery := query
			rangeQuery.FromBlock = new(big.Int).SetUint64(fromBlock)
			rangeQuery.ToBlock = new(big.Int).SetUint64(toBlock)

			logs, err := c.ec.FilterLogs(ctx, rangeQuery)
			if err != nil {
				log.Printf("base: poll: filter logs [%d..%d]: %v", fromBlock, toBlock, err)
				continue
			}

			for _, l := range logs {
				if l.Removed {
					continue
				}
				event, ok, parseErr := parseLog(l)
				if parseErr != nil {
					log.Printf("base: poll: parse log tx=%s: %v", l.TxHash.Hex(), parseErr)
					continue
				}
				if !ok {
					continue
				}
				handler(event)
			}

			// Advance cursor past the range we just processed.
			fromBlock = toBlock + 1
		}
	}
}

func (c *Client) SendTransaction(ctx context.Context, raw []byte) (string, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return "", fmt.Errorf("base: unmarshal tx: %w", err)
	}
	if err := c.ec.SendTransaction(ctx, tx); err != nil {
		return "", fmt.Errorf("base: send tx: %w", err)
	}
	return tx.Hash().Hex(), nil
}

func (c *Client) GasPrice(ctx context.Context) (*big.Int, error) {
	price, err := c.ec.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("base: gas price: %w", err)
	}
	return price, nil
}

// Close releases the underlying connections.
func (c *Client) Close() {
	c.ec.Close()
	if c.ws != nil {
		c.ws.Close()
	}
}
