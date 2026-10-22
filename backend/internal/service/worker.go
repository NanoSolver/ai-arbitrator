package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"gorm.io/gorm"

	"github.com/ai-arbitrator/backend/internal/ai"
	"github.com/ai-arbitrator/backend/internal/models"
)

const maxGradeTries = 3

// RunWorker drives each submission through: QUEUED -> GRADING -> SETTLING -> DONE.
// It is a single loop, so grading and transactions are naturally serialised.
func (s *Service) RunWorker(ctx context.Context) {
	log.Println("worker started")
	// A crash mid-grade leaves GRADING rows behind; grading is safe to repeat (no side effects yet).
	s.DB.Model(&models.Submission{}).Where("status = ?", models.SubGrading).Update("status", models.SubQueued)

	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.workOnce(ctx)
		}
	}
}

func (s *Service) workOnce(ctx context.Context) {
	// Settlement first: a pending oracle transaction must be resolved before anything else.
	var settling models.Submission
	if err := s.DB.Where("status = ?", models.SubSettling).Order("id").First(&settling).Error; err == nil {
		s.settle(ctx, &settling)
		return
	}

	var queued models.Submission
	if err := s.DB.Where("status = ?", models.SubQueued).Order("id").First(&queued).Error; err != nil {
		return
	}
	// Claim the row atomically.
	res := s.DB.Model(&models.Submission{}).
		Where("id = ? AND status = ?", queued.ID, models.SubQueued).
		Update("status", models.SubGrading)
	if res.Error != nil || res.RowsAffected != 1 {
		return
	}
	queued.Status = models.SubGrading
	s.grade(ctx, &queued)
}

func (s *Service) grade(ctx context.Context, sub *models.Submission) {
	var job models.Job
	if err := s.DB.First(&job, "id = ?", sub.JobID).Error; err != nil {
		s.fail(sub, "job not found")
		return
	}
	var criteria []ai.Criterion
	if err := json.Unmarshal([]byte(job.Criteria), &criteria); err != nil {
		s.fail(sub, "stored criteria are corrupt")
		return
	}

	gctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := s.AI.Grade(gctx, job.Description, criteria, sub.Content, sub.Type)
	if err != nil {
		sub.GradeTries++
		sub.Error = err.Error()
		if sub.GradeTries >= maxGradeTries {
			// The AI being down is NOT a reason to refund the client. The submission stays unresolved and
			// the freelancer keeps the contract's claimAfterTimeout path.
			sub.Status = models.SubFailed
			log.Printf("worker: grading submission %d failed permanently: %v", sub.ID, err)
		} else {
			sub.Status = models.SubQueued
			log.Printf("worker: grading submission %d failed (try %d/%d): %v", sub.ID, sub.GradeTries, maxGradeTries, err)
		}
		s.DB.Save(sub)
		if sub.Status == models.SubQueued {
			select { // back off so a flaky free-tier API is not hammered
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
		return
	}

	resultsJSON, _ := json.Marshal(res.CriteriaResults)
	sub.Verdict = res.Verdict
	sub.Reasoning = res.Reasoning
	sub.Feedback = res.Feedback
	sub.Results = string(resultsJSON)
	sub.Injection = res.InjectionDetected
	sub.Error = ""

	switch {
	case res.Verdict == "PASS":
		sub.Action = models.ActionRelease
	case job.Revisions < models.MaxRejections:
		sub.Action = models.ActionReject
	default:
		sub.Action = models.ActionRefund // revisions exhausted
	}
	log.Printf("worker: submission %d graded %s -> %s", sub.ID, res.Verdict, sub.Action)

	if s.Chain == nil {
		// Chain integration disabled (DEV_MODE): record the outcome without any transaction.
		sub.Status = models.SubSettling
		if err := s.applyOutcome(&job, sub, "dev-mode"); err != nil {
			s.fail(sub, "apply outcome: "+err.Error())
		}
		return
	}
	sub.Status = models.SubSettling
	if err := s.DB.Save(sub).Error; err != nil {
		log.Printf("worker: saving verdict for submission %d: %v", sub.ID, err)
	}
}

func (s *Service) settle(ctx context.Context, sub *models.Submission) {
	var job models.Job
	if err := s.DB.First(&job, "id = ?", sub.JobID).Error; err != nil {
		s.fail(sub, "job not found")
		return
	}
	if job.OnchainID == nil {
		s.fail(sub, "job has no on-chain id")
		return
	}

	// Send once. The tx hash is persisted immediately, so after a restart we resume waiting for the receipt
	// instead of sending a second transaction.
	if sub.TxHash == "" {
		sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		h, err := s.sendAction(sctx, *job.OnchainID, sub.Action)
		cancel()
		if err != nil {
			s.fail(sub, "oracle transaction failed: "+err.Error())
			return
		}
		sub.TxHash = h.Hex()
		if err := s.DB.Save(sub).Error; err != nil {
			log.Printf("worker: CRITICAL could not persist tx hash %s for submission %d: %v", sub.TxHash, sub.ID, err)
			return
		}
		log.Printf("worker: submission %d sent %s tx %s", sub.ID, sub.Action, sub.TxHash)
	}

	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	found, ok, err := s.Chain.Receipt(rctx, common.HexToHash(sub.TxHash))
	if err != nil {
		log.Printf("worker: receipt check for %s: %v", sub.TxHash, err)
		return
	}
	if !found {
		if time.Since(sub.UpdatedAt) > 15*time.Minute {
			s.fail(sub, "transaction not mined after 15 minutes: "+sub.TxHash)
		}
		return
	}
	if !ok {
		s.fail(sub, "transaction reverted: "+sub.TxHash)
		return
	}
	if err := s.applyOutcome(&job, sub, sub.TxHash); err != nil {
		log.Printf("worker: applying outcome for submission %d: %v", sub.ID, err)
		return
	}
	log.Printf("worker: submission %d settled (%s) tx %s", sub.ID, sub.Action, sub.TxHash)
}

func (s *Service) sendAction(ctx context.Context, onchainID uint64, action string) (common.Hash, error) {
	switch action {
	case models.ActionRelease:
		return s.Chain.ReleaseFunds(ctx, onchainID)
	case models.ActionReject:
		return s.Chain.RejectSubmission(ctx, onchainID)
	case models.ActionRefund:
		return s.Chain.RefundClient(ctx, onchainID)
	}
	return common.Hash{}, fmt.Errorf("unknown action %q", action)
}

// applyOutcome marks the submission done and moves the job to its next state in one DB transaction.
func (s *Service) applyOutcome(job *models.Job, sub *models.Submission, tx string) error {
	return s.DB.Transaction(func(db *gorm.DB) error {
		sub.Status = models.SubDone
		sub.TxHash = tx
		if err := db.Save(sub).Error; err != nil {
			return err
		}
		var updates map[string]any
		switch sub.Action {
		case models.ActionRelease:
			updates = map[string]any{"status": models.StatusReleased, "settle_tx": tx}
		case models.ActionReject:
			updates = map[string]any{"status": models.StatusRevisionRequested, "revisions": gorm.Expr("revisions + 1")}
		case models.ActionRefund:
			updates = map[string]any{"status": models.StatusRefunded, "settle_tx": tx}
		default:
			return fmt.Errorf("unknown action %q", sub.Action)
		}
		return db.Model(&models.Job{}).Where("id = ?", job.ID).Updates(updates).Error
	})
}

func (s *Service) fail(sub *models.Submission, msg string) {
	sub.Status = models.SubFailed
	sub.Error = msg
	if err := s.DB.Save(sub).Error; err != nil {
		log.Printf("worker: could not record failure for submission %d: %v", sub.ID, err)
	}
	log.Printf("worker: submission %d FAILED: %s", sub.ID, msg)
}
