package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	App    AppConfig
	L1     ChainConfig
	Base   ChainConfig
	Bridge BridgeConfig
	Log    LogConfig
}

type AppConfig struct {
	Name    string `env:"APP_NAME"    env-default:"erigon-l1-bridge"`
	Version string `env:"APP_VERSION" env-default:"1.0.0"`
	Env     string `env:"APP_ENV"     env-default:"development"`
}

type ChainConfig struct {
	RPCURL          string
	WSURL           string
	BridgeAddress   string
	PortalAddress   string
	PollIntervalSec uint64
}

type BridgeConfig struct {
	// SignerPrivateKey is the hex-encoded private key for submitting prove/finalize txs.
	// NEVER commit a real key. Use KMS or HSM in production.
	SignerPrivateKey string
	PaymasterKey     string
	PaymasterAddress string
	ClearingAddress  string
	// PortalAddress overrides the default Base OptimismPortal2 address.
	PortalAddress string
	// FactoryAddress overrides the default DisputeGameFactory address.
	FactoryAddress string
}

type LogConfig struct {
	Level string `env:"LOG_LEVEL" env-default:"info"`
}

func Load() (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			Name:    getEnv("APP_NAME", "erigon-l1-bridge"),
			Version: getEnv("APP_VERSION", "0.1.0"),
			Env:     getEnv("APP_ENV", "development"),
		},
		L1: ChainConfig{
			RPCURL:          getEnv("L1_RPC_URL", "http://localhost:8545"),
			WSURL:           getEnv("L1_WS_URL", "ws://localhost:8546"),
			BridgeAddress:   getEnv("L1_BRIDGE_ADDRESS", ""),
			PortalAddress:   getEnv("L1_PORTAL_ADDRESS", ""),
			PollIntervalSec: mustUint64(getEnv("L1_POLL_INTERVAL_SEC", "12")),
		},
		Base: ChainConfig{
			RPCURL:          getEnv("BASE_RPC_URL", "https://mainnet.base.org"),
			WSURL:           getEnv("BASE_WS_URL", ""),
			BridgeAddress:   getEnv("BASE_BRIDGE_ADDRESS", "0x4200000000000000000000000000000000000010"),
			PortalAddress:   getEnv("BASE_PORTAL_ADDRESS", "0x49048044D57e1C92A77f79988d21Fa8fAF74E97e"),
			PollIntervalSec: mustUint64(getEnv("BASE_POLL_INTERVAL_SEC", "2")),
		},
		Bridge: BridgeConfig{
			SignerPrivateKey: getEnv("BRIDGE_SIGNER_PRIVATE_KEY", ""),
			PaymasterKey:     getEnv("PAYMASTER_PRIVATE_KEY", ""),
			PaymasterAddress: getEnv("PAYMASTER_ADDRESS", ""),
			ClearingAddress:  getEnv("CLEARING_CONTRACT_ADDRESS", ""),
			PortalAddress:    getEnv("PORTAL_ADDRESS_OVERRIDE", ""),
			FactoryAddress:   getEnv("FACTORY_ADDRESS_OVERRIDE", ""),
		},
		Log: LogConfig{Level: getEnv("LOG_LEVEL", "info")},
	}

	if cfg.Bridge.SignerPrivateKey == "" && cfg.App.Env == "production" {
		return nil, fmt.Errorf("config: BRIDGE_SIGNER_PRIVATE_KEY required in production")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustUint64(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
