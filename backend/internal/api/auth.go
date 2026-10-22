package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/ai-arbitrator/backend/internal/models"
)

const (
	sessionCookie = "arbiter_session"
	sessionAge    = 30 * 24 * time.Hour
)

func accountJSON(account models.Account) gin.H {
	return gin.H{
		"id": account.ID, "name": account.Name, "email": account.Email,
		"role": account.Role, "wallet": account.Wallet,
	}
}

func (a *API) signUp(c *gin.Context) {
	var in struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Wallet   string `json:"wallet"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid sign-up details")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	parsedEmail, emailErr := mail.ParseAddress(in.Email)
	if in.Name == "" || len(in.Name) > 120 || len(in.Email) > 254 || emailErr != nil || parsedEmail.Address != in.Email {
		fail(c, http.StatusBadRequest, "enter a valid name and email address")
		return
	}
	if len(in.Password) < 8 || len(in.Password) > 72 {
		fail(c, http.StatusBadRequest, "password must be 8-72 bytes")
		return
	}
	if in.Role != "client" && in.Role != "freelancer" {
		fail(c, http.StatusBadRequest, "role must be client or freelancer")
		return
	}
	wallet, ok := normAddr(in.Wallet)
	if !ok {
		fail(c, http.StatusBadRequest, "wallet must be a valid 0x address")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "could not secure password")
		return
	}
	account := models.Account{Name: in.Name, Email: in.Email, PasswordHash: string(hash), Role: in.Role, Wallet: wallet}
	if err := a.S.DB.Create(&account).Error; err != nil {
		var existing models.Account
		if lookupErr := a.S.DB.Select("id").First(&existing, "email = ?", in.Email).Error; lookupErr == nil {
			fail(c, http.StatusConflict, "an account with that email already exists")
		} else {
			fail(c, http.StatusInternalServerError, "could not create account")
		}
		return
	}
	if !a.startSession(c, account) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"account": accountJSON(account)})
}

func (a *API) signIn(c *gin.Context) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	var account models.Account
	err := a.S.DB.First(&account, "email = ?", strings.ToLower(strings.TrimSpace(in.Email))).Error
	if err != nil || bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(in.Password)) != nil {
		fail(c, http.StatusUnauthorized, "email or password is incorrect")
		return
	}
	if !a.startSession(c, account) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"account": accountJSON(account)})
}

func (a *API) startSession(c *gin.Context, account models.Account) bool {
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		fail(c, http.StatusInternalServerError, "could not start session")
		return false
	}
	token := hex.EncodeToString(rawToken)
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().Add(sessionAge)
	if err := a.S.DB.Create(&models.AccountSession{
		TokenHash: hex.EncodeToString(tokenHash[:]), AccountID: account.ID, ExpiresAt: expiresAt,
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "could not start session")
		return false
	}
	secureCookie := c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookie, token, int(sessionAge.Seconds()), "/", "", secureCookie, true)
	return true
}

func (a *API) signOut(c *gin.Context) {
	if token, err := c.Cookie(sessionCookie); err == nil {
		hash := sha256.Sum256([]byte(token))
		_ = a.S.DB.Delete(&models.AccountSession{}, "token_hash = ?", hex.EncodeToString(hash[:])).Error
	}
	secureCookie := c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	c.SetCookie(sessionCookie, "", -1, "/", "", secureCookie, true)
	c.Status(http.StatusNoContent)
}

func (a *API) currentAccount(c *gin.Context) {
	account, ok := accountFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "sign in required")
		return
	}
	c.JSON(http.StatusOK, gin.H{"account": accountJSON(account)})
}

func (a *API) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookie)
		if err != nil || len(token) != 64 {
			fail(c, http.StatusUnauthorized, "sign in required")
			c.Abort()
			return
		}
		hash := sha256.Sum256([]byte(token))
		var session models.AccountSession
		if err := a.S.DB.First(&session, "token_hash = ? AND expires_at > ?", hex.EncodeToString(hash[:]), time.Now()).Error; err != nil {
			fail(c, http.StatusUnauthorized, "session expired; sign in again")
			c.Abort()
			return
		}
		var account models.Account
		if err := a.S.DB.First(&account, session.AccountID).Error; err != nil {
			fail(c, http.StatusUnauthorized, "account no longer exists")
			c.Abort()
			return
		}
		c.Set("account", account)
		c.Next()
	}
}

func accountFromContext(c *gin.Context) (models.Account, bool) {
	value, ok := c.Get("account")
	if !ok {
		return models.Account{}, false
	}
	account, ok := value.(models.Account)
	return account, ok
}

func ownsJob(account models.Account, job models.Job) bool {
	if account.Role == "client" {
		if job.ClientAccountID != nil && *job.ClientAccountID == account.ID {
			return true
		}
		return job.ClientAccountID == nil && account.Wallet != "" && strings.EqualFold(job.Client, account.Wallet)
	}
	return account.Role == "freelancer" && strings.EqualFold(job.Freelancer, account.Wallet)
}

func (a *API) requireRole(c *gin.Context, role string) bool {
	account, ok := accountFromContext(c)
	if !ok || account.Role != role {
		fail(c, http.StatusForbidden, "this action is not available for your account role")
		return false
	}
	return true
}

func (a *API) requireJobOwner(c *gin.Context, job models.Job) bool {
	account, ok := accountFromContext(c)
	if !ok || !ownsJob(account, job) {
		fail(c, http.StatusNotFound, "job not found")
		return false
	}
	return true
}
