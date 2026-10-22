package api

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ai-arbitrator/backend/internal/ai"
	"github.com/ai-arbitrator/backend/internal/config"
	"github.com/ai-arbitrator/backend/internal/models"
	"github.com/ai-arbitrator/backend/internal/service"
)

const (
	maxContentChars = 20000
	maxCriteria     = 10
)

type API struct {
	S   *service.Service
	Cfg config.Config
}

func New(s *service.Service, cfg config.Config) *API { return &API{S: s, Cfg: cfg} }

func (a *API) Routes(r *gin.Engine) {
	r.Use(cors.New(cors.Config{
		AllowOrigins:     a.Cfg.CORSOrigins,
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Content-Type"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	g := r.Group("/api")
	g.GET("/config", a.getConfig)
	g.POST("/auth/signup", a.signUp)
	g.POST("/auth/signin", a.signIn)
	authenticated := g.Group("")
	authenticated.Use(a.requireSession())
	authenticated.GET("/auth/me", a.currentAccount)
	authenticated.POST("/auth/signout", a.signOut)
	authenticated.POST("/jobs", a.createJob)
	authenticated.GET("/jobs", a.listJobs)
	authenticated.GET("/jobs/:id", a.getJob)
	authenticated.POST("/jobs/:id/confirm", a.confirmJob)
	authenticated.POST("/jobs/:id/submissions", a.createSubmission)
	authenticated.POST("/jobs/:id/retry", a.retryFailedSubmission)
	authenticated.GET("/jobs/:id/verdict", a.getVerdict)

	if a.Cfg.DevMode {
		authenticated.POST("/dev/jobs/:id/fund", a.devFund)
	}
}

// ───────────────────────── helpers ─────────────────────────

func fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg})
}

func normAddr(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !common.IsHexAddress(s) {
		return "", false
	}
	return strings.ToLower(common.HexToAddress(s).Hex()), true
}

func (a *API) loadJob(c *gin.Context) (*models.Job, bool) {
	var job models.Job
	err := a.S.DB.First(&job, "id = ?", c.Param("id")).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "job not found")
		return nil, false
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return nil, false
	}
	if !a.requireJobOwner(c, job) {
		return nil, false
	}
	return &job, true
}

func jobView(j models.Job) gin.H {
	var criteria []ai.Criterion
	_ = json.Unmarshal([]byte(j.Criteria), &criteria)
	return gin.H{
		"id":                  j.ID,
		"onchainId":           j.OnchainID,
		"title":               j.Title,
		"description":         j.Description,
		"client":              j.Client,
		"freelancer":          j.Freelancer,
		"criteria":            criteria,
		"requirementsHash":    j.RequirementsHash,
		"requirementsPayload": j.RequirementsPayload,
		"status":              j.Status,
		"amountWei":           j.AmountWei,
		"deadline":            j.Deadline,
		"revisions":           j.Revisions,
		"maxRevisions":        models.MaxRejections,
		"settleTx":            j.SettleTx,
		"createdAt":           j.CreatedAt,
	}
}

func subView(s models.Submission, withContent bool) gin.H {
	var results []ai.CriterionResult
	_ = json.Unmarshal([]byte(s.Results), &results)
	v := gin.H{
		"id":                s.ID,
		"type":              s.Type,
		"hash":              s.Hash,
		"attempt":           s.Attempt,
		"status":            s.Status,
		"verdict":           s.Verdict,
		"action":            s.Action,
		"reasoning":         s.Reasoning,
		"feedback":          s.Feedback,
		"criteriaResults":   results,
		"injectionDetected": s.Injection,
		"txHash":            s.TxHash,
		"error":             s.Error,
		"createdAt":         s.CreatedAt,
	}
	if withContent {
		v["content"] = s.Content
	}
	return v
}

// ───────────────────────── handlers ─────────────────────────

func (a *API) getConfig(c *gin.Context) {
	out := gin.H{
		"chainId":         a.Cfg.ChainID,
		"contractAddress": a.Cfg.ContractAddress,
		"devMode":         a.Cfg.DevMode,
		"chainEnabled":    a.S.Chain != nil,
	}
	if a.S.Chain != nil {
		out["oracleAddress"] = strings.ToLower(a.S.Chain.OracleAddress().Hex())
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/jobs: the client describes the job; the AI proposes an acceptance rubric.
func (a *API) createJob(c *gin.Context) {
	if !a.requireRole(c, "client") {
		return
	}
	var in struct {
		Title       string `json:"title" binding:"required"`
		Description string `json:"description" binding:"required"`
		Freelancer  string `json:"freelancer" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "title, description and freelancer are required")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	if len(in.Title) == 0 || len(in.Title) > 200 {
		fail(c, http.StatusBadRequest, "title must be 1-200 characters")
		return
	}
	if len(in.Description) < 10 || len(in.Description) > 5000 {
		fail(c, http.StatusBadRequest, "description must be 10-5000 characters")
		return
	}
	freelancer, ok := normAddr(in.Freelancer)
	if !ok {
		fail(c, http.StatusBadRequest, "freelancer must be a valid 0x address")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	criteria, err := a.S.AI.Rubric(ctx, in.Title, in.Description)
	if err != nil {
		fail(c, http.StatusBadGateway, "could not generate rubric: "+err.Error())
		return
	}
	criteriaJSON, _ := json.Marshal(criteria)

	account, _ := accountFromContext(c)
	clientAccountID := account.ID
	job := models.Job{
		ID:              service.RandHex(16),
		ClientAccountID: &clientAccountID,
		Title:           in.Title,
		Description:     in.Description,
		Client:          account.Wallet,
		Freelancer:      freelancer,
		Criteria:        string(criteriaJSON),
		Status:          models.StatusDraft,
	}
	if err := a.S.DB.Create(&job).Error; err != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return
	}
	c.JSON(http.StatusCreated, jobView(job))
}

// POST /api/jobs/:id/confirm: the client approves (optionally edits) the rubric. We hash the exact payload;
// the client then passes requirementsHash to createJob on-chain.
func (a *API) confirmJob(c *gin.Context) {
	if !a.requireRole(c, "client") {
		return
	}
	job, ok := a.loadJob(c)
	if !ok {
		return
	}
	if job.Status != models.StatusDraft {
		fail(c, http.StatusConflict, "job is not in DRAFT state")
		return
	}

	var in struct {
		Criteria []struct {
			Text     string `json:"text"`
			Required *bool  `json:"required"`
		} `json:"criteria"`
	}
	_ = c.ShouldBindJSON(&in) // body is optional

	var criteria []ai.Criterion
	if len(in.Criteria) > 0 {
		if len(in.Criteria) > maxCriteria {
			fail(c, http.StatusBadRequest, "too many criteria")
			return
		}
		for i, cr := range in.Criteria {
			text := strings.TrimSpace(cr.Text)
			if text == "" || len(text) > 500 {
				fail(c, http.StatusBadRequest, "each criterion needs 1-500 characters of text")
				return
			}
			required := true
			if cr.Required != nil {
				required = *cr.Required
			}
			criteria = append(criteria, ai.Criterion{ID: "c" + strconv.Itoa(i+1), Text: text, Required: required})
		}
		hasRequired := false
		for _, cr := range criteria {
			hasRequired = hasRequired || cr.Required
		}
		if !hasRequired {
			fail(c, http.StatusBadRequest, "at least one criterion must be required")
			return
		}
	} else {
		_ = json.Unmarshal([]byte(job.Criteria), &criteria)
	}
	criteriaJSON, _ := json.Marshal(criteria)

	// The salt makes the hash unique per job even if two clients post identical text.
	payload := struct {
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Freelancer  string         `json:"freelancer"`
		Criteria    []ai.Criterion `json:"criteria"`
		Salt        string         `json:"salt"`
	}{job.Title, job.Description, job.Freelancer, criteria, service.RandHex(16)}
	payloadJSON, _ := json.Marshal(payload)
	hash := crypto.Keccak256Hash(payloadJSON).Hex()

	res := a.S.DB.Model(&models.Job{}).
		Where("id = ? AND status = ?", job.ID, models.StatusDraft).
		Updates(map[string]any{
			"criteria":             string(criteriaJSON),
			"requirements_payload": string(payloadJSON),
			"requirements_hash":    hash,
			"status":               models.StatusAwaitingFunding,
		})
	if res.Error != nil || res.RowsAffected != 1 {
		fail(c, http.StatusConflict, "could not confirm job (already confirmed?)")
		return
	}
	a.S.DB.First(job, "id = ?", job.ID)
	c.JSON(http.StatusOK, jobView(*job))
}

func (a *API) listJobs(c *gin.Context) {
	account, ok := accountFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "sign in required")
		return
	}
	q := a.S.DB.Model(&models.Job{})
	if account.Role == "client" {
		q = q.Where("client_account_id = ? OR (client_account_id IS NULL AND client = ?)", account.ID, account.Wallet)
	} else {
		q = q.Where("freelancer = ? AND status <> ?", account.Wallet, models.StatusDraft)
	}
	var jobs []models.Job
	if err := q.Order("created_at desc").Limit(100).Find(&jobs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return
	}
	out := make([]gin.H, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobView(j))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getJob(c *gin.Context) {
	job, ok := a.loadJob(c)
	if !ok {
		return
	}
	var subs []models.Submission
	a.S.DB.Where("job_id = ?", job.ID).Order("id desc").Limit(10).Find(&subs)
	views := make([]gin.H, 0, len(subs))
	for _, s := range subs {
		views = append(views, subView(s, true))
	}
	v := jobView(*job)
	v["submissions"] = views
	c.JSON(http.StatusOK, v)
}

// POST /api/jobs/:id/submissions: the freelancer uploads the work. We store it and return its hash;
// the frontend then calls submitWork(onchainJobId, submissionHash) on-chain, and the listener starts grading
// once that transaction is confirmed. The oracle therefore only ever acts on an on-chain Submitted job.
func (a *API) createSubmission(c *gin.Context) {
	if !a.requireRole(c, "freelancer") {
		return
	}
	job, ok := a.loadJob(c)
	if !ok {
		return
	}
	if job.Status != models.StatusFunded && job.Status != models.StatusRevisionRequested {
		fail(c, http.StatusConflict, "job is not accepting submissions (status "+job.Status+")")
		return
	}

	var in struct {
		Content string `json:"content" binding:"required"`
		Type    string `json:"type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "content is required")
		return
	}
	if in.Type == "" {
		in.Type = "text"
	}
	if in.Type != "text" && in.Type != "code" && in.Type != "link" {
		fail(c, http.StatusBadRequest, "type must be text, code or link")
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" || len(in.Content) > maxContentChars {
		fail(c, http.StatusBadRequest, "content must be 1-20000 characters")
		return
	}
	if in.Type == "link" && !strings.HasPrefix(in.Content, "http://") && !strings.HasPrefix(in.Content, "https://") {
		fail(c, http.StatusBadRequest, "link submissions must start with http:// or https://")
		return
	}

	sub := models.Submission{
		JobID:   job.ID,
		Type:    in.Type,
		Content: in.Content,
		Hash:    crypto.Keccak256Hash([]byte(in.Content)).Hex(),
		Status:  models.SubAwaitingChain,
	}
	if a.Cfg.DevMode && a.S.Chain == nil {
		sub.Status = models.SubQueued // no chain to wait for
	}

	err := a.S.DB.Transaction(func(tx *gorm.DB) error {
		// Only the newest not-yet-on-chain submission can match the freelancer's on-chain hash.
		if err := tx.Model(&models.Submission{}).
			Where("job_id = ? AND status = ?", job.ID, models.SubAwaitingChain).
			Update("status", models.SubSuperseded).Error; err != nil {
			return err
		}
		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		if sub.Status == models.SubQueued {
			return tx.Model(&models.Job{}).Where("id = ?", job.ID).Update("status", models.StatusSubmitted).Error
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"submissionId":   sub.ID,
		"submissionHash": sub.Hash,
		"onchainJobId":   job.OnchainID,
		"status":         sub.Status,
	})
}

// GET /api/jobs/:id/verdict: the latest submission that has reached a verdict (or is in progress).
func (a *API) getVerdict(c *gin.Context) {
	job, ok := a.loadJob(c)
	if !ok {
		return
	}
	var sub models.Submission
	err := a.S.DB.Where("job_id = ? AND status <> ?", job.ID, models.SubSuperseded).Order("id desc").First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusOK, gin.H{"jobStatus": job.Status, "submission": nil})
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobStatus": job.Status, "submission": subView(sub, false)})
}

// POST /api/jobs/:id/retry requeues the latest failed grading attempt. It cannot repeat a settlement.
func (a *API) retryFailedSubmission(c *gin.Context) {
	if !a.requireRole(c, "client") {
		return
	}
	job, ok := a.loadJob(c)
	if !ok {
		return
	}
	if job.Status != models.StatusSubmitted {
		fail(c, http.StatusConflict, "job is not awaiting a review")
		return
	}
	var sub models.Submission
	if err := a.S.DB.Where("job_id = ? AND status <> ?", job.ID, models.SubSuperseded).Order("id desc").First(&sub).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "submission not found")
		} else {
			fail(c, http.StatusInternalServerError, "database error")
		}
		return
	}
	if sub.Status != models.SubFailed || sub.Action != "" || sub.TxHash != "" {
		fail(c, http.StatusConflict, "only a failed grading attempt can be retried")
		return
	}
	result := a.S.DB.Model(&models.Submission{}).
		Where("id = ? AND status = ? AND action = ? AND tx_hash = ?", sub.ID, models.SubFailed, "", "").
		Updates(map[string]any{"status": models.SubQueued, "grade_tries": 0, "error": ""})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return
	}
	if result.RowsAffected != 1 {
		fail(c, http.StatusConflict, "submission is no longer available to retry")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"submissionId": sub.ID, "status": models.SubQueued})
}

// POST /api/dev/jobs/:id/fund (DEV_MODE only): pretend the client funded the job on-chain.
func (a *API) devFund(c *gin.Context) {
	if !a.requireRole(c, "client") {
		return
	}
	job, ok := a.loadJob(c)
	if !ok {
		return
	}
	if job.Status != models.StatusAwaitingFunding {
		fail(c, http.StatusConflict, "job must be AWAITING_FUNDING (confirm the rubric first)")
		return
	}
	account, _ := accountFromContext(c)
	client := account.Wallet
	if client == "" {
		client = common.BigToAddress(new(big.Int).SetUint64(uint64(account.ID))).Hex()
	}
	client, _ = normAddr(client)
	var maxID uint64
	a.S.DB.Model(&models.Job{}).Select("COALESCE(MAX(onchain_id), 0)").Scan(&maxID)
	err := a.S.DB.Model(&models.Job{}).Where("id = ?", job.ID).Updates(map[string]any{
		"onchain_id": maxID + 1,
		"client":     client,
		"amount_wei": "1000000000000000",
		"deadline":   time.Now().Add(7 * 24 * time.Hour).Unix(),
		"status":     models.StatusFunded,
	}).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "database error")
		return
	}
	a.S.DB.First(job, "id = ?", job.ID)
	c.JSON(http.StatusOK, jobView(*job))
}
