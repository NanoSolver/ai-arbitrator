package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ai-arbitrator/backend/internal/ai"
	"github.com/ai-arbitrator/backend/internal/api"
	"github.com/ai-arbitrator/backend/internal/chain"
	"github.com/ai-arbitrator/backend/internal/config"
	"github.com/ai-arbitrator/backend/internal/models"
	"github.com/ai-arbitrator/backend/internal/service"
)

func main() {
	cfg := config.Load()

	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{Logger: quietLogger()})
	if err != nil {
		log.Fatalf("database: %v (is `docker compose up -d` running?)", err)
	}
	if err := db.AutoMigrate(&models.Account{}, &models.AccountSession{}, &models.Job{}, &models.Submission{}, &models.ChainState{}); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var ch *chain.Chain
	if cfg.ChainEnabled() {
		dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		ch, err = chain.New(dialCtx, cfg.RPCURL, cfg.ContractAddress, cfg.OraclePrivateKey, cfg.ChainID)
		cancel()
		if err != nil {
			log.Fatalf("chain: %v", err)
		}
		log.Printf("chain enabled: contract %s, oracle %s", ch.ContractAddress().Hex(), ch.OracleAddress().Hex())
	} else if cfg.DevMode {
		log.Println("WARNING: chain disabled, running in DEV_MODE (no real transactions)")
	} else {
		log.Fatal("chain not configured: set RPC_URL, CONTRACT_ADDRESS and ORACLE_PRIVATE_KEY, or set DEV_MODE=true")
	}

	svc := service.New(db, ai.New(cfg.AIServiceURL, cfg.InternalAPIKey), ch, cfg)
	go svc.RunWorker(ctx)
	if ch != nil {
		go svc.RunListener(ctx)
	}

	if !cfg.DevMode {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()
	api.New(svc, cfg).Routes(r)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: r}
	go func() {
		log.Printf("API listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

// quietLogger keeps GORM's warnings and errors but stops logging "record not found",
// which is normal when the worker polls an empty queue.
func quietLogger() logger.Interface {
	return logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
		SlowThreshold:             500 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
	})
}
