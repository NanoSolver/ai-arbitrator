package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ai-arbitrator/backend/internal/chain"
	"github.com/ai-arbitrator/backend/internal/models"
)

const cursorName = "last_block"

// RunListener polls contract logs over plain HTTP (works with any RPC, including free tiers).
func (s *Service) RunListener(ctx context.Context) {
	log.Println("chain listener started")
	t := time.NewTicker(6 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.pollOnce(ctx); err != nil {
				log.Printf("listener: %v", err)
			}
		}
	}
}

func (s *Service) pollOnce(ctx context.Context) error {
	latest, err := s.Chain.LatestBlock(ctx)
	if err != nil {
		return fmt.Errorf("latest block: %w", err)
	}
	if latest < s.Cfg.Confirmations {
		return nil
	}
	safe := latest - s.Cfg.Confirmations

	last, err := s.cursor(latest)
	if err != nil {
		return err
	}

	// Catch up in bounded chunks; free RPC tiers cap the eth_getLogs range.
	for i := 0; i < 100 && last < safe; i++ {
		from := last + 1
		to := from + s.Cfg.LogChunk - 1
		if to > safe {
			to = safe
		}
		logs, err := s.Chain.Logs(ctx, from, to)
		if err != nil {
			return fmt.Errorf("logs %d-%d: %w", from, to, err)
		}
		for _, l := range logs {
			ev, err := s.Chain.Decode(l)
			if err != nil {
				log.Printf("listener: skipping undecodable log in block %d: %v", l.BlockNumber, err)
				continue
			}
			// A DB error aborts the poll WITHOUT advancing the cursor, so the block is retried.
			if err := s.handle(ev); err != nil {
				return fmt.Errorf("handle event in block %d: %w", l.BlockNumber, err)
			}
		}
		last = to
		if err := s.setCursor(last); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) cursor(latest uint64) (uint64, error) {
	var st models.ChainState
	err := s.DB.First(&st, "name = ?", cursorName).Error
	if err == nil {
		v, perr := strconv.ParseUint(st.Value, 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("corrupt cursor %q", st.Value)
		}
		return v, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if s.Cfg.DeployBlock > 0 {
		return s.Cfg.DeployBlock - 1, nil
	}
	log.Printf("listener: DEPLOY_BLOCK not set, starting from current block %d (earlier events will be missed)", latest)
	return latest, nil
}

func (s *Service) setCursor(v uint64) error {
	st := models.ChainState{Name: cursorName, Value: strconv.FormatUint(v, 10)}
	return s.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&st).Error
}

func (s *Service) handle(ev any) error {
	switch e := ev.(type) {
	case chain.JobFunded:
		return s.onJobFunded(e)
	case chain.WorkSubmitted:
		return s.onWorkSubmitted(e)
	}
	return nil
}

// onJobFunded links an on-chain job to the rubric the client confirmed. The match is by requirements hash
// AND freelancer address, so someone re-using a public hash with a different freelancer is ignored.
func (s *Service) onJobFunded(e chain.JobFunded) error {
	res := s.DB.Model(&models.Job{}).
		Where("requirements_hash = ? AND status = ? AND freelancer = ?",
			e.ReqHash.Hex(), models.StatusAwaitingFunding, strings.ToLower(e.Freelancer.Hex())).
		Updates(map[string]any{
			"onchain_id": e.JobID,
			"client":     strings.ToLower(e.Client.Hex()),
			"amount_wei": e.Amount.String(),
			"deadline":   int64(e.Deadline),
			"status":     models.StatusFunded,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		log.Printf("listener: JobFunded for on-chain job %d matched no awaiting job (hash %s)", e.JobID, e.ReqHash.Hex())
	} else {
		log.Printf("listener: job funded on-chain id=%d amount=%s wei", e.JobID, e.Amount.String())
	}
	return nil
}

// onWorkSubmitted links the freelancer's on-chain submitWork to the stored submission and queues it for grading.
func (s *Service) onWorkSubmitted(e chain.WorkSubmitted) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var job models.Job
		err := tx.First(&job, "onchain_id = ?", e.JobID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("listener: WorkSubmitted for unknown on-chain job %d", e.JobID)
			return nil
		}
		if err != nil {
			return err
		}
		if job.Status != models.StatusFunded && job.Status != models.StatusRevisionRequested {
			return nil // already processed (idempotent replay)
		}

		res := tx.Model(&models.Submission{}).
			Where("job_id = ? AND hash = ? AND status = ?", job.ID, e.SubHash.Hex(), models.SubAwaitingChain).
			Updates(map[string]any{"status": models.SubQueued, "attempt": int(e.Attempt)})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// The freelancer submitted a hash we never stored. Nothing to grade; on-chain they can still
			// claim after the review timeout, which is the contract's safety valve.
			log.Printf("listener: WorkSubmitted for job %d has no matching stored submission (hash %s)", e.JobID, e.SubHash.Hex())
			return nil
		}
		return tx.Model(&models.Job{}).Where("id = ?", job.ID).Update("status", models.StatusSubmitted).Error
	})
}
