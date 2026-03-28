package main

import (
	"context"
	"crypto/ecdsa"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/erigontech/erigon-l1-bridge/config"
	basebridge "github.com/erigontech/erigon-l1-bridge/internal/bridge/base"
	baseclient "github.com/erigontech/erigon-l1-bridge/internal/chain/base"
	"github.com/erigontech/erigon-l1-bridge/internal/clearing"
	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/finality"
	"github.com/erigontech/erigon-l1-bridge/internal/listener"
	"github.com/erigontech/erigon-l1-bridge/internal/paymaster"
	"github.com/erigontech/erigon-l1-bridge/internal/repo/memory"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
	"github.com/erigontech/erigon-l1-bridge/pkg/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	l := logger.New(cfg.Log.Level, cfg.App.Name, cfg.App.Version, cfg.App.Env)

	l.Info().Str("env", cfg.App.Env).Msgf("starting erigon-l1-bridge", cfg.App.Name, cfg.App.Version)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── repositories ─────────────────────────────────────────────────────────
	eventRepo := memory.NewEventStore()
	clearingRepo := memory.NewClearingStore()

	// ── paymaster ─────────────────────────────────────────────────────────────
	pm := paymaster.New([]byte(cfg.Bridge.PaymasterKey))
	pm.RegisterChain(entity.ChainIDBase, cfg.Bridge.PaymasterAddress)

	// ── clearing use case ─────────────────────────────────────────────────────
	uc := usecase.NewClearingUseCase(eventRepo, clearingRepo, pm)

	// ── Base L2 chain client ───────────────────────────────────────────────────
	baseClient, err := baseclient.New(baseclient.Config{
		RPCURL:       cfg.Base.RPCURL,
		WSURL:        cfg.Base.WSURL,
		PollInterval: time.Duration(cfg.Base.PollIntervalSec) * time.Second,
	})
	if err != nil {
		log.Fatalf("base client: %v", err)
	}
	defer baseClient.Close()

	// ── Build bridge adapter ───────────────────────────────────────────────────
	// If a signer key is configured, wire the full adapter capable of submitting
	// prove/finalize transactions. Otherwise use the read-only stub (safe for
	// development without real ETH on L1).
	var bridgeAdapter usecase.BridgeAdapter

	if cfg.Bridge.SignerPrivateKey != "" {
		l.Info().Msg("signer key present — wiring full bridge adapter")

		signerKey, err := loadPrivateKey(cfg.Bridge.SignerPrivateKey)
		if err != nil {
			log.Fatalf("load signer key: %v", err)
		}

		// L1 client — needed for prove/finalize submissions.
		l1Client, err := ethclient.Dial(cfg.L1.RPCURL)
		if err != nil {
			log.Fatalf("l1 client: %v", err)
		}
		defer l1Client.Close()

		// Raw L2 RPC client — needed for eth_getProof (not exposed by ethclient).
		l2RPC, err := rpc.Dial(cfg.Base.RPCURL)
		if err != nil {
			log.Fatalf("l2 rpc: %v", err)
		}
		defer l2RPC.Close()
		l2Direct, err := ethclient.Dial(cfg.Base.RPCURL)
		if err != nil {
			log.Fatalf("l2 direct client: %v", err)
		}
		defer l2Direct.Close()

		bridgeAdapter, err = basebridge.NewFromConfig(basebridge.AdapterConfig{
			L1Client:       l1Client,
			L2Client:       l2Direct,
			L2RPCClient:    l2RPC,
			Signer:         signerKey,
			PortalAddress:  cfg.Bridge.PortalAddress,
			FactoryAddress: cfg.Bridge.FactoryAddress,
		})
		if err != nil {
			log.Fatalf("bridge adapter: %v", err)
		}
	} else {
		l.Info().Msg("no signer key — using read-only bridge adapter (observation mode)")
		bridgeAdapter = basebridge.New(cfg.Base.PortalAddress)
	}

	// ── finality + adapter registration ──────────────────────────────────────
	baseFinalityCfg := entity.FinalityConfig{
		ChainID:            entity.ChainIDBase,
		SafeConfirmations:  10,
		FinalizedDelay:     50,
		ProofWindowSeconds: 604800, // 7 days
	}
	uc.RegisterAdapter(bridgeAdapter)
	uc.RegisterFinalityChecker(finality.New(baseClient, baseFinalityCfg))

	// ── listener ──────────────────────────────────────────────────────────────
	lst := listener.New(uc)
	lst.Register(baseClient)

	if err := lst.Start(ctx); err != nil {
		log.Fatalf("listener: %v", err)
	}

	// ── clearing orchestrator ─────────────────────────────────────────────────
	go clearing.NewOrchestrator(uc, 15*time.Second).Run(ctx)

	l.Info().Msg("bridge running — watching Base for withdrawal events")
	<-ctx.Done()
	l.Info().Msg("shutting down")
}

func loadPrivateKey(hexKey string) (*ecdsa.PrivateKey, error) {
	// Strip 0x prefix if present.
	key := hexKey
	if len(key) >= 2 && key[:2] == "0x" {
		key = key[2:]
	}
	return crypto.HexToECDSA(key)
}
