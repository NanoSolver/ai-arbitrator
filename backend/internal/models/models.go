package models

import "time"

// Job statuses. The contract's own state (Funded/Submitted/Released/Refunded) is mirrored from
// chain events and from the oracle's own confirmed transactions.
const (
	StatusDraft             = "DRAFT"              // rubric proposed by AI, client has not confirmed
	StatusAwaitingFunding   = "AWAITING_FUNDING"   // rubric confirmed + hashed, waiting for createJob on-chain
	StatusFunded            = "FUNDED"             // JobFunded event seen
	StatusSubmitted         = "SUBMITTED"          // WorkSubmitted event seen, grading/settling in progress
	StatusRevisionRequested = "REVISION_REQUESTED" // AI rejected; freelancer may resubmit
	StatusReleased          = "RELEASED"
	StatusRefunded          = "REFUNDED"
)

// Submission statuses.
const (
	SubAwaitingChain = "AWAITING_CHAIN" // stored; waiting for the freelancer's on-chain submitWork
	SubQueued        = "QUEUED"         // on-chain submission confirmed; waiting for the grader
	SubGrading       = "GRADING"
	SubSettling      = "SETTLING" // verdict stored; oracle transaction pending
	SubDone          = "DONE"
	SubFailed        = "FAILED"
	SubSuperseded    = "SUPERSEDED" // replaced by a newer submission before it reached the chain
)

// Oracle actions.
const (
	ActionRelease = "RELEASE"
	ActionReject  = "REJECT"
	ActionRefund  = "REFUND"
)

// MaxRejections mirrors AIArbitrator.MAX_REJECTIONS in the contract.
const MaxRejections = 3

type Job struct {
	ID                  string  `gorm:"primaryKey;size:32"` // random, unguessable API identifier
	ClientAccountID     *uint   `gorm:"index"`
	OnchainID           *uint64 `gorm:"uniqueIndex"`
	Title               string
	Description         string  `gorm:"type:text"`
	Client              string  `gorm:"index"`       // lowercase 0x address; set from the JobFunded event
	Freelancer          string  `gorm:"index"`       // lowercase 0x address; chosen by the client
	Criteria            string  `gorm:"type:text"`   // JSON array of {id,text,required}
	RequirementsPayload string  `gorm:"type:text"`   // exact JSON that was hashed
	RequirementsHash    *string `gorm:"uniqueIndex"` // keccak256(payload) as 0x hex
	Status              string  `gorm:"index"`
	AmountWei           string
	Deadline            int64
	Revisions           int
	SettleTx            string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Submission struct {
	ID         uint   `gorm:"primaryKey"`
	JobID      string `gorm:"index;size:32"`
	Type       string // text | code | link
	Content    string `gorm:"type:text"`
	Hash       string `gorm:"index"` // keccak256(content), 0x hex; must match the on-chain submissionHash
	Attempt    int
	Status     string `gorm:"index"`
	GradeTries int
	Verdict    string
	Action     string
	Reasoning  string `gorm:"type:text"`
	Feedback   string `gorm:"type:text"`
	Results    string `gorm:"type:text"` // JSON array of per-criterion results
	Injection  bool
	TxHash     string
	Error      string `gorm:"type:text"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ChainState stores listener progress (last processed block).
type ChainState struct {
	Name  string `gorm:"primaryKey"`
	Value string
}

// Account stores registered application users. Passwords are bcrypt hashes only.
type Account struct {
	ID           uint   `gorm:"primaryKey"`
	Name         string `gorm:"size:120;not null"`
	Email        string `gorm:"size:254;not null;uniqueIndex"`
	PasswordHash string `gorm:"size:60;not null" json:"-"`
	Role         string `gorm:"size:16;not null;index"`
	Wallet       string `gorm:"size:42;not null;index"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AccountSession stores only a SHA-256 hash of the opaque browser session token.
type AccountSession struct {
	TokenHash string    `gorm:"primaryKey;size:64"`
	AccountID uint      `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}
