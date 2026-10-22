package service

import (
	"crypto/rand"
	"encoding/hex"

	"gorm.io/gorm"

	"github.com/ai-arbitrator/backend/internal/ai"
	"github.com/ai-arbitrator/backend/internal/chain"
	"github.com/ai-arbitrator/backend/internal/config"
)

// Service holds the long-running pieces: the chain listener and the grading/settlement worker.
// Chain is nil when the chain integration is disabled (DEV_MODE without a deployed contract).
type Service struct {
	DB    *gorm.DB
	AI    *ai.Client
	Chain *chain.Chain
	Cfg   config.Config
}

func New(db *gorm.DB, aiClient *ai.Client, ch *chain.Chain, cfg config.Config) *Service {
	return &Service{DB: db, AI: aiClient, Chain: ch, Cfg: cfg}
}

// RandHex returns n random bytes as lowercase hex (2n characters).
func RandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
