package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port           string
	DatabaseURL    string
	AIServiceURL   string
	InternalAPIKey string
	CORSOrigins    []string

	// Chain settings. If RPCURL, ContractAddress or OraclePrivateKey is empty the chain
	// integration is disabled (useful together with DevMode).
	RPCURL           string
	ChainID          int64
	ContractAddress  string
	DeployBlock      uint64
	OraclePrivateKey string
	LogChunk         uint64 // max block range per eth_getLogs call (free RPC tiers are strict)
	Confirmations    uint64

	// DevMode lets you exercise the whole AI pipeline without a deployed contract.
	DevMode bool
}

func Load() Config {
	_ = godotenv.Load()

	c := Config{
		Port:             get("PORT", "8080"),
		DatabaseURL:      get("DATABASE_URL", ""),
		AIServiceURL:     strings.TrimRight(get("AI_SERVICE_URL", "http://127.0.0.1:8000"), "/"),
		InternalAPIKey:   get("INTERNAL_API_KEY", ""),
		CORSOrigins:      split(get("CORS_ORIGINS", "http://localhost:5173,http://localhost:3000")),
		RPCURL:           get("RPC_URL", ""),
		ChainID:          int64(getUint("CHAIN_ID", 11155111)),
		ContractAddress:  get("CONTRACT_ADDRESS", ""),
		DeployBlock:      getUint("DEPLOY_BLOCK", 0),
		OraclePrivateKey: get("ORACLE_PRIVATE_KEY", ""),
		LogChunk:         getUint("LOG_CHUNK", 9),
		Confirmations:    getUint("CONFIRMATIONS", 1),
		DevMode:          get("DEV_MODE", "false") == "true",
	}

	if c.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if c.InternalAPIKey == "" {
		log.Fatal("INTERNAL_API_KEY is required (must match the AI service)")
	}
	if c.LogChunk == 0 {
		c.LogChunk = 9
	}
	return c
}

func (c Config) ChainEnabled() bool {
	return c.RPCURL != "" && c.ContractAddress != "" && c.OraclePrivateKey != ""
}

func get(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getUint(key string, def uint64) uint64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		log.Fatalf("%s must be a non-negative integer, got %q", key, v)
	}
	return n
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
